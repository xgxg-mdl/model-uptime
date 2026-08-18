# Model Uptime

Model Uptime describes the observable health of configured model services over time.

## Language

**Health Heatmap**:
A per-service two-dimensional time grid showing each period as healthy, slow, failing, or unobserved.
_Avoid_: Cross-service status matrix, latency heatmap, uptime chart

**Heatmap Cell**:
An aggregate health state for one time bucket with at least 50% of its expected samples. Green tolerates slow samples below 20%, yellow represents slow samples at or above 20% or failures below 20%, red represents failures at or above 20%, and gray means unobserved or insufficiently observed.
_Avoid_: Probe result, raw sample

**Status Timeline Slot**:
One completed, interval-aligned time bucket for one service. The current partial interval is excluded, and bucket boundaries advance only when a complete interval elapses. A completed observation cycle covers from its probe start until the later of its nominal interval or completion; an active request covers elapsed complete buckets as probing. A slot aggregates overlapping cycle coverage and is healthy, slow, failing, probing, paused, unobserved, or before the service observation lifecycle.
_Avoid_: Raw history item, sample index
