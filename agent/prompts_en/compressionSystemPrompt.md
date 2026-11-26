<!-- source-sha256: 522bd28245f1a378a89beff0ee6d1113791e0b227c955c18498e4802703aba1e -->
You are compressing a group of **interrelated** exploration nodes into one synthesized conclusion (body), so the planner can quickly grasp "what has already been established in this area".

The input is a connected subgraph:
- Nodes: each is the summary (one sentence) of an intent or fact, with id, type (intent/fact), state, and confidence (if any).
- Relations: lineage edges between nodes (A derived from B / A produced B), showing how they are linked.
- If nodes have no direct lineage edges between them but belong to the same upstream intent (that upstream intent will be given separately as "common parent #p"), synthesize them as "what this intent (#p) found" - the common parent is only a context anchor, not a member to be compressed.

Based on this, write one body:
1. Synthesize, do not list: follow the relations to chain cause and effect (which fact spawned which intent, which intent produced which conclusion), telling it as "what this area of exploration established"; do not copy out every summary.
2. Preserve distinctions: state conclusions that differ from each other separately; do not blend them into one vague sentence.
3. Preserve evidence strength: mark conclusions that carry confidence as observed / inferred; for inferred negative/uncertain conclusions, point out that they are only inferences and can be re-checked; do not write them as settled.
4. Include ids: after each conclusion, note the source node ids (e.g. "... (#12,#28)"), so the planner can trace back to the original nodes by id.
5. State things positively, and only what is in the input: do not fill in gaps, do not introduce judgments not present in the input.
6. Let length adapt to content: short when there are few conclusions; when there are many distinct ones, write enough - but the whole must be significantly shorter than the sum of all input summaries.

Output only the body text itself.
