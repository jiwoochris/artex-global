package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yukiteruamano/artex/db"
)

// TestNotifierDeliveryReasonsLocalized guards F17: every delivery failure/defer
// reason that notifier.go writes into notification_deliveries.last_error — which
// notify_api.go echoes back to the "delivery log" table in delivery-list.tsx — must be
// the exact English source message. Diagnostic log.Printf lines stay in the original
// language (Z2) and are intentionally not covered here.
func TestNotifierDeliveryReasonsLocalized(t *testing.T) {
	// Every constant is formatted with the same ASCII arguments the runtime uses, and
	// the expected English template is written out here rather than read back from the
	// constant, so a reworded or re-localized literal fails.
	cases := []struct {
		label       string
		msg         string
		wantEnglish string
	}{
		{"channelKindUnregistered", fmt.Sprintf(errDeliveryChannelKindUnregistered, "webhook"),
			`Unregistered channel type: "webhook"`},
		{"snapshotUnrenderable", errDeliverySnapshotUnrenderable,
			"Could not parse the event snapshot, so this vulnerability could not be turned into a message"},
		{"channelLengthCapped", fmt.Sprintf(errDeliveryChannelLengthCapped, 3),
			"This message reached the channel length limit, so only the first 3 were sent and the rest are deferred to the next batch"},
		{"noDeliveredCount", fmt.Sprintf(errDeliveryNoDeliveredCount, 0),
			"The channel did not report the number of messages sent (delivered=0)"},
		{"retryExhausted", fmt.Sprintf(errDeliveryRetryExhausted, db.MaxNotifyAttempts, errors.New("boom")),
			fmt.Sprintf("Still failed after %d retries: boom", db.MaxNotifyAttempts)},
		{"snapshotEmpty", fmt.Sprintf(errDeliverySnapshotEmpty, 7),
			"The event snapshot for delivery item #7 is empty"},
		{"snapshotParse", fmt.Errorf(errDeliverySnapshotParse, 9, errors.New("unexpected end of JSON input")).Error(),
			"Failed to parse the event snapshot of delivery item #9: unexpected end of JSON input"},
		{"batchAllUnparseable", fmt.Sprintf(errDeliveryBatchAllUnparseable, 2),
			"Failed to parse all 2 deliveries in the summary batch"},
	}
	for _, c := range cases {
		assertEnglishMessage(t, c.label, c.msg, c.wantEnglish)
	}
}

// TestNotifierParseSnapshotErrorsLocalized exercises the real parseSnapshot paths
// that surface as last_error through renderSingle → FailDeliveries. parseSnapshot is
// a package-level pure function, so no DB or Server is needed.
func TestNotifierParseSnapshotErrorsLocalized(t *testing.T) {
	// Empty snapshot.
	if _, err := parseSnapshot(&db.NotificationDelivery{ID: 7}); err == nil {
		t.Fatal("an empty snapshot must not pass without an error")
	} else {
		assertEnglishMessage(t, "parseSnapshot.empty", err.Error(), "The event snapshot for delivery item #7 is empty")
		if !strings.Contains(err.Error(), "7") {
			t.Fatalf("parseSnapshot.empty: delivery entry ID 7 is not in the message: %q", err.Error())
		}
	}
	// Malformed JSON snapshot.
	if _, err := parseSnapshot(&db.NotificationDelivery{ID: 9, Snapshot: []byte("{bad")}); err == nil {
		t.Fatal("a broken JSON snapshot must not pass without an error")
	} else {
		// The wrapped json decoder detail varies by Go version, so only the source prefix is pinned.
		assertEnglishPrefix(t, "parseSnapshot.malformed", err.Error(),
			"Failed to parse the event snapshot of delivery item #9: ")
	}
}

// TestNotifierRenderBatchAllUnparseableLocalized drives the real renderBatch "every
// snapshot is unparseable" branch. With all snapshots empty, every delivery is
// skipped before itemFor (and thus before n.pg), so a zero-value Notifier reaches
// the errDeliveryBatchAllUnparseable return without touching the database.
func TestNotifierRenderBatchAllUnparseableLocalized(t *testing.T) {
	n := &Notifier{}
	deliveries := []*db.NotificationDelivery{{ID: 1}, {ID: 2}}
	if _, _, err := n.renderBatch(context.Background(), deliveries, "", 30); err == nil {
		t.Fatal("an error must be returned when every snapshot is unparseable")
	} else {
		assertEnglishMessage(t, "renderBatch.allUnparseable", err.Error(),
			"Failed to parse all 2 deliveries in the summary batch")
	}
}
