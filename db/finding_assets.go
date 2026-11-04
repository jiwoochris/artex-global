package db

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Finding asset tree - the "per asset" view of the global findings list.
//
// The hierarchy comes from the same source as BuildCoverageGraph (company ->
// root_domain/ip/app -> subdomain -> service -> endpoint), but that one is a
// force-directed graph of "assets in scope for one task" while this one is a tree
// of "assets that have findings anywhere in the database": it only includes assets
// with findings plus their ancestor chain, and nodes carry aggregated subtree
// counts. The parent preference rules of both must stay identical; if you change
// one, change the other in task_scope.go as well.
// ---------------------------------------------------------------------------

// FindingUnassignedAsset is the node key of the "unassigned assets" bucket and
// also the filter sentinel of the list endpoint: it matches findings whose
// asset_ids is empty or whose referenced assets have been deleted.
const FindingUnassignedAsset = "__none__"

// findingAssetTreeMaxNodes caps the number of nodes returned to the frontend.
// Above it, whole layers are dropped bottom-up (endpoint first, then service):
// their counts are already accumulated into the parent, so dropping nodes never
// drops numbers.
const findingAssetTreeMaxNodes = 3000

// FindingAssetNode is one node of the asset tree. Keys are isomorphic with the
// coverage graph: an asset row is "a:<id>", a company is "c:<id>", a root domain
// with no asset row is a synthetic "r:<domain>", and the unassigned bucket is
// "__none__".
type FindingAssetNode struct {
	Key       string `json:"key"`
	Parent    string `json:"parent,omitempty"`
	Kind      string `json:"kind"` // company|root_domain|subdomain|ip|service|app|endpoint|none
	Label     string `json:"label"`
	AssetID   int64  `json:"asset_id,omitempty"`
	CompanyID int64  `json:"company_id,omitempty"`
	// Self is the number of findings attached directly to this asset; Total covers
	// all descendants and is deduplicated per finding (a finding on several assets
	// counts once, on their common ancestor).
	Self        int       `json:"self"`
	Total       int       `json:"total"`
	Critical    int       `json:"critical"`
	High        int       `json:"high"`
	Medium      int       `json:"medium"`
	Low         int       `json:"low"`
	LastFoundAt time.Time `json:"last_found_at"`
}

// FindingAssetTree is a one-shot snapshot of the whole tree. Nodes is already
// sorted: within one parent by finding count descending then label ascending, and
// "unassigned assets" always last.
type FindingAssetTree struct {
	Nodes        []FindingAssetNode `json:"nodes"`
	FindingTotal int                `json:"finding_total"`
	// Truncated=true means layers in DroppedKinds were dropped to bound the size.
	Truncated    bool     `json:"truncated"`
	DroppedKinds []string `json:"dropped_kinds,omitempty"`
}

// assetRow is the subset of asset columns needed to build the tree.
type assetRow struct {
	id          int64
	kind        string
	companyID   int64
	domain      string
	rootDomain  string
	ip          string
	url         string
	port        int
	serviceType string
	appName     string
}

func (a *assetRow) coverageNode() CoverageGraphNode {
	return CoverageGraphNode{
		Kind: a.kind, Domain: a.domain, RootDomain: a.rootDomain, IP: a.ip,
		URL: a.url, Port: a.port, ServiceType: a.serviceType, AppName: a.appName,
	}
}

// label reuses the coverage graph label rules (URL > domain > ip > app_name >
// root_domain), but a service without a URL (SMB, non-HTTP ports, ...) needs its
// port appended: otherwise its label is identical to the host IP/domain row and the
// two tree rows look like an exact duplicate.
func (a *assetRow) label() string {
	if a.kind == "service" && a.url == "" {
		if host, port := a.hostPort(); host != "" && port > 0 {
			return host + ":" + strconv.Itoa(port)
		}
	}
	n := a.coverageNode()
	n.Key = assetKey(a.id)
	return coverageNodeLabel(&n)
}

// hostPort matches the coverage graph: domain first, then the host inside URL,
// finally ip.
func (a *assetRow) hostPort() (string, int) {
	n := a.coverageNode()
	return hostPortOf(&n)
}

const findingAssetSelectCols = `a.id, a.type, COALESCE(a.company_id,0),
       COALESCE(a.domain,''), COALESCE(a.root_domain,''), COALESCE(a.ip,''),
       COALESCE(a.url,''), COALESCE(a.port,0), COALESCE(a.service_type,''),
       COALESCE(a.app_name,'')`

func scanAssetRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]*assetRow, error) {
	defer rows.Close()
	var out []*assetRow
	for rows.Next() {
		a := &assetRow{}
		if err := rows.Scan(&a.id, &a.kind, &a.companyID, &a.domain, &a.rootDomain,
			&a.ip, &a.url, &a.port, &a.serviceType, &a.appName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// findingAssetHit is the minimum information about a finding needed while building
// the tree.
type findingAssetHit struct {
	severity string
	ts       time.Time
	assetIDs []int64
}

// BuildFindingAssetTree builds the asset tree for the current filter. AssetScope
// itself is not applied (otherwise the tree would collapse into a single chain
// around the selected node).
func (d *DB) BuildFindingAssetTree(f FindingFilter) (*FindingAssetTree, error) {
	return d.buildFindingAssetTree(f, findingAssetTreeMaxNodes)
}

// buildFindingAssetTree is the internal implementation with a node cap. maxNodes<=0
// disables truncation - resolving AssetScope must use that mode, otherwise the
// dropped endpoints leave the subtree id set incomplete.
func (d *DB) buildFindingAssetTree(f FindingFilter, maxNodes int) (*FindingAssetTree, error) {
	f.AssetScope = ""
	f.assetIDs, f.assetNone, f.assetMiss = nil, false, false
	where, args := f.where()

	rows, err := d.Query(`SELECT COALESCE(f.severity,''), f.created_at,
       COALESCE(f.asset_ids::text,'[]')
FROM findings f LEFT JOIN tasks t ON f.task_id = t.id`+where, args...)
	if err != nil {
		return nil, err
	}
	hits := []findingAssetHit{}
	assetIDs := map[int64]bool{}
	for rows.Next() {
		var h findingAssetHit
		var aidsJSON string
		if err := rows.Scan(&h.severity, &h.ts, &aidsJSON); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(aidsJSON), &h.assetIDs)
		for _, id := range h.assetIDs {
			if id > 0 {
				assetIDs[id] = true
			}
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	tree := &FindingAssetTree{Nodes: []FindingAssetNode{}, FindingTotal: len(hits)}
	byID, err := d.loadFindingAssetRows(assetIDs)
	if err != nil {
		return nil, err
	}

	nodes, parentOf := d.assembleFindingAssetNodes(byID)
	if err := d.attachCompanyNodes(nodes, parentOf); err != nil {
		return nil, err
	}

	// Counting: each finding walks up the ancestor chain of every asset it is attached
	// to, collects the deduplicated key set and adds 1 to each, so a parent is not
	// counted twice when one finding is attached to several child assets.
	unassigned := &FindingAssetNode{Key: FindingUnassignedAsset, Kind: "none", Label: "Unassigned assets"}
	touched := map[string]bool{}
	for _, h := range hits {
		clear(touched)
		var direct []*FindingAssetNode
		for _, id := range h.assetIDs {
			node := nodes[assetKey(id)]
			if node == nil {
				continue
			}
			direct = append(direct, node)
			for key := node.Key; key != ""; key = parentOf[key] {
				touched[key] = true
			}
		}
		if len(direct) == 0 {
			countFinding(unassigned, h)
			unassigned.Self++
			continue
		}
		for _, node := range direct {
			node.Self++
		}
		for key := range touched {
			countFinding(nodes[key], h)
		}
	}

	for _, node := range nodes {
		if node.Total > 0 {
			tree.Nodes = append(tree.Nodes, *node)
		}
	}
	if unassigned.Total > 0 {
		tree.Nodes = append(tree.Nodes, *unassigned)
	}
	sortFindingAssetNodes(tree.Nodes)
	truncateFindingAssetTree(tree, maxNodes)
	return tree, nil
}

// countFinding accumulates one finding onto a node (total / severity buckets / last
// found time).
func countFinding(n *FindingAssetNode, h findingAssetHit) {
	if n == nil {
		return
	}
	n.Total++
	switch h.severity {
	case "critical":
		n.Critical++
	case "high":
		n.High++
	case "medium":
		n.Medium++
	case "low":
		n.Low++
	}
	if h.ts.After(n.LastFoundAt) {
		n.LastFoundAt = h.ts
	}
}

// loadFindingAssetRows loads the matched asset rows and fills in ancestors round by
// round (the host domain/IP of a service, the root domain of a subdomain). Ancestors
// may have no findings of their own, but the tree needs them to take shape.
func (d *DB) loadFindingAssetRows(ids map[int64]bool) (map[int64]*assetRow, error) {
	byID := map[int64]*assetRow{}
	if len(ids) == 0 {
		return byID, nil
	}
	idList := make([]int64, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	rows, err := d.Query(`SELECT `+findingAssetSelectCols+` FROM assets a WHERE a.id = ANY($1::bigint[])`, idList)
	if err != nil {
		return nil, err
	}
	found, err := scanAssetRows(rows)
	if err != nil {
		return nil, err
	}
	for _, a := range found {
		byID[a.id] = a
	}

	// Each round finds the host identifiers whose parent is still missing and loads
	// one layer in bulk; the number of layers is fixed
	// (endpoint->service->subdomain/ip->root_domain), so 4 rounds always converge.
	for range 4 {
		want := missingParents(byID)
		if want.empty() {
			break
		}
		added, err := d.loadAssetsByHost(want, byID)
		if err != nil {
			return nil, err
		}
		if added == 0 {
			break
		}
	}
	return byID, nil
}

// missingHosts holds the host identifiers to look up in one fill round, split by
// target asset type.
type missingHosts struct {
	services []string // host of an endpoint (looks for the service row)
	domains  []string // host domain of a service/endpoint (looks for the subdomain row)
	ips      []string // host IP of a service/endpoint (looks for the ip row)
	roots    []string // root domain of a subdomain (looks for the root_domain row)
}

func (m missingHosts) empty() bool {
	return len(m.services) == 0 && len(m.domains) == 0 && len(m.ips) == 0 && len(m.roots) == 0
}

// missingParents collects hosts that are not loaded yet: services (for endpoints to
// attach to), subdomains/IPs (for services and endpoints) and root domains (for
// subdomains).
func missingParents(byID map[int64]*assetRow) missingHosts {
	haveService := map[string]bool{}
	haveDomain := map[string]bool{}
	haveIP := map[string]bool{}
	haveRoot := map[string]bool{}
	for _, a := range byID {
		switch a.kind {
		case "service":
			if host, _ := a.hostPort(); host != "" {
				haveService[host] = true
			}
		case "subdomain":
			haveDomain[a.domain] = true
		case "ip":
			haveIP[a.ip] = true
		case "root_domain":
			haveRoot[a.domain] = true
		}
	}
	wantService := map[string]bool{}
	wantDomain := map[string]bool{}
	wantIP := map[string]bool{}
	wantRoot := map[string]bool{}
	for _, a := range byID {
		switch a.kind {
		case "service", "endpoint":
			host, _ := a.hostPort()
			// An endpoint looks for a service on the same host first; services whose port
			// does not match end up with Total=0 and are filtered out, so they cannot
			// pollute the tree.
			if a.kind == "endpoint" && host != "" && !haveService[host] {
				wantService[host] = true
			}
			if host != "" && !haveDomain[host] && !haveIP[host] && !haveRoot[host] {
				if isIPLiteral(host) {
					wantIP[host] = true
				} else {
					wantDomain[host] = true
				}
			}
			if a.ip != "" && !haveIP[a.ip] {
				wantIP[a.ip] = true
			}
		case "subdomain":
			if a.rootDomain != "" && !haveRoot[a.rootDomain] {
				wantRoot[a.rootDomain] = true
			}
		}
	}
	return missingHosts{
		services: keysOf(wantService),
		domains:  keysOf(wantDomain),
		ips:      keysOf(wantIP),
		roots:    keysOf(wantRoot),
	}
}

func keysOf(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// isIPLiteral roughly decides whether a host is an IP literal (used to pick the ip
// or the subdomain table when looking for a host).
func isIPLiteral(host string) bool {
	if strings.Contains(host, ":") {
		return true // IPv6
	}
	if host == "" {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return strings.Count(host, ".") == 3
}

// loadAssetsByHost loads asset rows in bulk by host identifier and returns how many
// rows this round added.
func (d *DB) loadAssetsByHost(want missingHosts, byID map[int64]*assetRow) (int, error) {
	added := 0
	load := func(q string, arg []string) error {
		if len(arg) == 0 {
			return nil
		}
		rows, err := d.Query(q, arg)
		if err != nil {
			return err
		}
		found, err := scanAssetRows(rows)
		if err != nil {
			return err
		}
		for _, a := range found {
			if _, ok := byID[a.id]; ok {
				continue
			}
			byID[a.id] = a
			added++
		}
		return nil
	}
	if err := load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='service' AND (a.domain = ANY($1::text[]) OR a.ip = ANY($1::text[]))`, want.services); err != nil {
		return 0, err
	}
	if err := load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='subdomain' AND a.domain = ANY($1::text[])`, want.domains); err != nil {
		return 0, err
	}
	if err := load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='ip' AND a.ip = ANY($1::text[])`, want.ips); err != nil {
		return 0, err
	}
	if err := load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='root_domain' AND a.domain = ANY($1::text[])`, want.roots); err != nil {
		return 0, err
	}
	return added, nil
}

// assembleFindingAssetNodes turns asset rows into nodes and wires up parent/child
// relations. When a parent is missing (the database has no such root domain asset at
// all) a synthetic "r:<domain>" placeholder node is created, consistent with the
// coverage graph.
func (d *DB) assembleFindingAssetNodes(byID map[int64]*assetRow) (map[string]*FindingAssetNode, map[string]string) {
	nodes := map[string]*FindingAssetNode{}
	parentOf := map[string]string{}
	rootByDomain := map[string]string{}
	subByDomain := map[string]string{}
	ipByAddr := map[string]string{}
	svcByHost := map[string]string{}
	svcByHostPort := map[string]string{}

	for _, a := range byID {
		key := assetKey(a.id)
		nodes[key] = &FindingAssetNode{
			Key: key, Kind: a.kind, Label: a.label(),
			AssetID: a.id, CompanyID: a.companyID,
		}
		switch a.kind {
		case "root_domain":
			if a.domain != "" {
				rootByDomain[a.domain] = key
			}
		case "subdomain":
			if a.domain != "" {
				subByDomain[a.domain] = key
			}
		case "ip":
			if a.ip != "" {
				ipByAddr[a.ip] = key
			}
		case "service":
			if host, port := a.hostPort(); host != "" {
				svcByHost[host] = key
				svcByHostPort[host+"|"+strconv.Itoa(port)] = key
			}
		}
	}

	// When a subdomain's root domain has no asset row, synthesise a placeholder root
	// so the subdomain does not end up scattered at the top level.
	for _, a := range byID {
		if a.kind != "subdomain" || a.rootDomain == "" {
			continue
		}
		if _, ok := rootByDomain[a.rootDomain]; ok {
			continue
		}
		key := "r:" + a.rootDomain
		nodes[key] = &FindingAssetNode{Key: key, Kind: "root_domain", Label: a.rootDomain}
		rootByDomain[a.rootDomain] = key
	}

	firstOf := func(keys ...string) string {
		for _, k := range keys {
			if k != "" {
				if _, ok := nodes[k]; ok {
					return k
				}
			}
		}
		return ""
	}
	for _, a := range byID {
		key := assetKey(a.id)
		var parent string
		switch a.kind {
		case "subdomain":
			parent = firstOf(rootByDomain[a.rootDomain])
		case "service":
			host, _ := a.hostPort()
			parent = firstOf(subByDomain[a.domain], subByDomain[host],
				ipByAddr[a.ip], ipByAddr[host], rootByDomain[a.rootDomain], rootByDomain[host])
		case "endpoint":
			host, port := a.hostPort()
			parent = firstOf(svcByHostPort[host+"|"+strconv.Itoa(port)], svcByHost[host],
				subByDomain[host], subByDomain[a.domain], ipByAddr[host], ipByAddr[a.ip],
				rootByDomain[a.rootDomain], rootByDomain[host])
		}
		if parent != "" && parent != key {
			parentOf[key] = parent
			nodes[key].Parent = parent
		}
	}
	return nodes, parentOf
}

// attachCompanyNodes adds a company parent to top-level assets (root domain / IP /
// app) - the company layer only appears when the asset really belongs to a company;
// unassigned assets stay top level themselves.
func (d *DB) attachCompanyNodes(nodes map[string]*FindingAssetNode, parentOf map[string]string) error {
	want := map[int64]bool{}
	for _, n := range nodes {
		if n.Parent != "" || n.CompanyID <= 0 {
			continue
		}
		switch n.Kind {
		case "root_domain", "ip", "app":
			want[n.CompanyID] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	rows, err := d.Query(`SELECT id, COALESCE(name,'') FROM companies WHERE id = ANY($1::bigint[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id, name := range names {
		key := companyKey(id)
		if _, ok := nodes[key]; ok {
			continue
		}
		if name == "" {
			name = "Company #" + strconv.FormatInt(id, 10)
		}
		nodes[key] = &FindingAssetNode{Key: key, Kind: "company", Label: name, CompanyID: id}
	}
	for _, n := range nodes {
		if n.Parent != "" || n.CompanyID <= 0 || n.Kind == "company" {
			continue
		}
		switch n.Kind {
		case "root_domain", "ip", "app":
			key := companyKey(n.CompanyID)
			if _, ok := nodes[key]; !ok {
				continue
			}
			n.Parent = key
			parentOf[n.Key] = key
		}
	}
	return nil
}

// sortFindingAssetNodes sorts by finding count descending then label; "unassigned
// assets" always last. The frontend attaches children in array order, so only the
// relative order within one parent has to be correct.
func sortFindingAssetNodes(nodes []FindingAssetNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if (a.Kind == "none") != (b.Kind == "none") {
			return b.Kind == "none"
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Label < b.Label
	})
}

// truncateFindingAssetTree drops whole layers when there are too many nodes
// (endpoint first, then service). Counts are already accumulated on the parent, so
// only the expandable detail layers are lost.
func truncateFindingAssetTree(tree *FindingAssetTree, maxNodes int) {
	if maxNodes <= 0 || len(tree.Nodes) <= maxNodes {
		return
	}
	for _, kind := range []string{"endpoint", "service"} {
		kept := tree.Nodes[:0]
		for _, n := range tree.Nodes {
			if n.Kind == kind {
				continue
			}
			kept = append(kept, n)
		}
		tree.Nodes = kept
		tree.Truncated = true
		tree.DroppedKinds = append(tree.DroppedKinds, kind)
		if len(tree.Nodes) <= maxNodes {
			return
		}
	}
}

// applyAssetScope resolves AssetScope (a node key) into an asset id set usable in
// SQL. Selecting a node selects its whole subtree, so the tree has to be built first
// and its descendants collected.
func (d *DB) applyAssetScope(f FindingFilter) (FindingFilter, error) {
	scope := strings.TrimSpace(f.AssetScope)
	f.assetIDs, f.assetNone, f.assetMiss = nil, false, false
	if scope == "" {
		return f, nil
	}
	if scope == FindingUnassignedAsset {
		f.assetNone = true
		return f, nil
	}
	// No truncation: dropped endpoints must still contribute ids, otherwise the list
	// loses data.
	tree, err := d.buildFindingAssetTree(f, 0)
	if err != nil {
		return f, err
	}
	children := map[string][]FindingAssetNode{}
	byKey := map[string]FindingAssetNode{}
	for _, n := range tree.Nodes {
		byKey[n.Key] = n
		children[n.Parent] = append(children[n.Parent], n)
	}
	if _, ok := byKey[scope]; !ok {
		// The selected node no longer exists under the current filter; the result must be
		// empty rather than degrading into no filtering at all.
		f.assetMiss = true
		return f, nil
	}
	seen := map[string]bool{scope: true}
	queue := []string{scope}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if id := byKey[key].AssetID; id > 0 {
			f.assetIDs = append(f.assetIDs, id)
		}
		for _, child := range children[key] {
			if seen[child.Key] {
				continue
			}
			seen[child.Key] = true
			queue = append(queue, child.Key)
		}
	}
	if len(f.assetIDs) == 0 {
		f.assetMiss = true
	}
	return f, nil
}

// assetIDContainments turns asset ids into right-hand operands of jsonb containment
// checks, used together with idx_findings_asset_ids (GIN jsonb_path_ops).
func assetIDContainments(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "["+strconv.FormatInt(id, 10)+"]")
	}
	return out
}
