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
One interval-wide rolling time bucket for one service. A completed observation cycle covers from its probe start until the later of its nominal interval or completion; an active request covers time as probing. A slot aggregates overlapping cycle coverage and is healthy, slow, failing, probing, paused, unobserved, or before the service observation lifecycle.
_Avoid_: Raw history item, sample index
