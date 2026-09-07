# First Windows storage experiment

Date: 2026-09-07. Mac controller/client to a Windows 11 Home IdeaPad (i7-10750H, 12 logical CPUs, 16 GB RAM). The native Windows agent used a dedicated directory on F:. Controller and file traffic crossed SSH tunnels over the home Wi-Fi. The laptop reported 2.4 GHz, 802.11n, and approximately -70 dBm signal during one sample.

## Observed

- The Windows executable ran and reported inventory.
- Enrollment succeeded. Separate processes reloaded the DPAPI-protected identity and sent two heartbeats with persisted, increasing sequence numbers.
- A 128 MiB upload reached completed publication. Its return download advanced very slowly and was deliberately canceled before full integrity verification.
- A later 8 MiB upload stopped after 4 MiB with HTTP 503. The exercised sequential path indicated unavailable authorization/publication, but no controller-side root cause was established.
- Independent SSH deployment and cleanup commands were also slow or timed out. Splitting control and data into separate SSH connections did not establish a successful repeat: enrollment failed in that attempt.
- The entire remote recovery/integrity suite did **not** pass. There are no valid end-to-end Windows throughput or crash-recovery claims from this session.

Low Wi-Fi throughput, SSH transport behavior and Mesh request overhead were not independently isolated. The observed signal/rate values alone do not establish the cause. Do not attribute all of the slowdown to Mesh or claim that the later local optimizations resolved this Windows setup.

Test schemas and per-run remote directories were cleaned up; one failed cleanup required explicitly stopping its leftover agent before removing the directory. A temporary cached executable, `F:\Mesh-Test-Agent-bd5fbb6b.exe`, was retained to avoid repeated slow deployment and still needs removal when laptop access resumes. No Windows service was installed.

## Repeat when the laptop is available

Measure a small raw SSH upload/download baseline first, plus a wired or stronger wireless connection if available. Then run native storage through trusted direct HTTPS or separate control/data tunnels, capturing controller error causes without credentials. Compare the old and optimized agents on the same fixtures and connection. Verify native and retrieved hashes, missing/occupied destination handling, revocation, forced process termination and resumed offsets. Test reboot/sleep separately. Cross-compilation and local race tests do not replace these checks.
