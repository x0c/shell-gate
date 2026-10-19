# Mac stuck on “Connecting…” — storage and swap exhaustion before forced restart

Workspace toast: `Connecting to mac; waking it if asleep…` (from `ensureMachineReady` → `POST /api/machines/mac/ready`). Edge overlay probe to Mac ttyd fails (`No route to host` / timeout). Optional presence reports `comfort_home=false` (wake not suppressed).

## Ruling (2026-10-15)

The initial router-only investigation established LAN unreachability, without distinguishing shutdown, deep sleep, disconnection or an unresponsive OS. Later local evidence supersedes the initial shutdown/deep-sleep hypotheses for this incident: APFS ran out of space, new swap files could not be created, and the kernel repeatedly terminated processes for low swap. The owner confirmed that opening the MacBook gave no response and that they manually forced a restart at 22:49. This strongly supports resource exhaustion as the cause of the unresponsive machine and remote failure; no final hang stack was captured.

General diagnostic method and the open prevention gap belong to the shared [Mac performance guide](~/.config/agentsync/docs/MAC_PERFORMANCE_GOVERNANCE_GUIDE.md#remote-wake-failure-with-an-unresponsive-local-mac-2026-10-15). No application, power, network or cleanup configuration was changed during this investigation.

## Verified diagnostic order

1. **UI path** — toast appears ~250 ms into `ensureMachineReady`; the request waits on `upstreamWaker.ensureReady()` (wake command ≤ 25 s, then TCP poll ≤ 50 s).
2. **Presence** — `GET` the configured presence URL. If `comfort_home` is true, expect a fast home-suppress failure, not a long connect toast. Query failure is fail-open (wake still attempted).
3. **Edge → Mac overlay** — TCP to the Mac ttyd target from the gateway. Sleep often yields `No route to host` on the tunnel; that only authorizes entering the wake branch.
4. **Relay wake** — run the fixed wake entry (`SHELL_WAKE_COMMANDS` for `mac`). Exit 0 means magic packet / LAN SSH nudge was attempted, not that ttyd is up.
5. **Home router** (direct LAN reachability evidence):
   - DHCP lease / hostname still present ≠ online (leases linger until expiry).
   - Neighbor/ARP `FAILED` or `INCOMPLETE`, ping loss, and absence from the LAN bridge MAC table ⇒ no current router-observed LAN response; this does not independently prove that Wi-Fi is disconnected or power is off.
   - Stale IPv6 neighbor entries for the Mac’s Wi‑Fi MAC only prove it was seen earlier.
6. **Re-check after wake** — if ARP/ping/bridge still dead, stop retrying gateway soft-wake; local power / Wi-Fi / OS state must be checked. If opening the lid also gives no response, inspect pre-restart disk and memory-pressure logs rather than assuming ordinary sleep.

## What this is not

- At-home wake suppression (that path returns a dedicated “at home” message and must not send magic packets).
- ttyd / compression / WebSocket validity issues (those need the Mac already reachable).
- A tunnel-only outage while the Mac answers on the home LAN IP/SSH.

## Evidence snapshot (2026-10-15)

| Check | Result |
|---|---|
| Presence | `comfort_home=false`, `network_home=false` |
| Edge → Mac ttyd (overlay) | fail (`No route to host` / timeout) |
| Relay wake entry | exit 0 |
| Router DHCP | lease for Mac hostname still valid |
| Router ARP / neigh for Mac LAN IP | `FAILED` / `INCOMPLETE` |
| Router ping / bridge MAC | 100% loss; Mac MAC absent from bridge table |
| After wake | LAN still no response |

## Local evidence after recovery (all times CST, 2026-10-15)

| Time | Observed evidence |
|---|---|
| 10:04:40 | Last `DarkWake`; later assertions and diagnostic reports prove execution continued. This was not the shutdown time. |
| 15:35 / 17:44 | ANECompilerService resource reports recorded approximately 3.75 GB / 1,984 MB free disk space; not attribution to a consuming workload. |
| 18:49:34 onward | APFS returned `ENOSPC` and `No space left on device` for data-volume writes. |
| 19:10:47 | `cloudd` exited because its metadata database could not be created: database or disk full. |
| 19:57:46–20:00:09 | Kernel logged `vnode_setsize for swap files failed: 28` and `low swap: failed to create swapfile`; APFS allocation failures also affected the VM volume. |
| 19:57:47–20:00:15 | Retrieved records contain 3,116 `killing_idle_process ... (low-swap ...)` lines; this counts log records, not unique applications or proven unique kills. |
| 20:02–20:04 | Wi-Fi disassociation and auto-join activity; critical memory pressure through 20:03:56 and power assertions through 20:03:57. |
| 22:49:22 | Current boot began. Owner confirms a manual forced restart after opening the lid gave no response. |
| 22:49:49 | `ResetCounter-2026-10-15-224949.diag`: `Boot faults: btn_rst,finger_reset force_off`. No matching kernel-panic report was found. |
| After restart | A resource report recorded about 30.24 GB free; this is a recovery reading, not pre-failure availability. |

Evidence sources: `pmset -g log`, bounded `/usr/bin/log show` queries, `last reboot shutdown`, `sysctl kern.boottime`, and `/Library/Logs/DiagnosticReports/`. Relevant resource reports are `ANECompilerService report at 15:37:05`, `ANECompilerService report at 17:46:40`, and the reset report above. Raw diagnostics stay on the Mac and must not be committed because they include private machine/network data.

[Apple's Activity Monitor guide](https://support.apple.com/guide/activity-monitor/actmntr1004/mac) identifies swap as startup-disk storage for RAM. The [Apple XNU swap implementation](https://github.com/apple-oss-distributions/xnu/blob/main/osfmk/vm/vm_compressor_backing_store.c) contains the failed-creation message and disk-exhaustion checks; it explains the mechanism but is not asserted to match installed macOS 27.2 exactly.

Limits: no preserved final hang stack or complete per-process memory/storage inventory exists. ENOSPC victims and low-swap termination targets do not establish a single offending app. Wi-Fi activity coincided with the resource failure; its independent cause is unproven. No cleanup or preventive fix was performed.

## Related

- Domain KB: [Mac wake and lease](../MAC_WAKE_LEASE_KNOWLEDGE_BASE.md) (§1 wake sequence, §6 pitfalls).
- Install / wake scripts: [Mac upstream guide](../MAC_UPSTREAM_GUIDE.md).
- Home suppress design: [wake suppress when home](../design/2026-09-09-wake-suppress-when-home.md).
