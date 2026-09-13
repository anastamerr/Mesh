# Connectivity foundation validation

Date: 2026-09-08.

## Implemented behavior

- Paired nodes can opt into private-LAN publication with `run --direct-lan`.
- The controller stores bounded candidate observations and returns them only while the node is fresh.
- Managed clients authenticate candidate TLS against the paired device key before path selection.
- Up to four direct attempts receive a 250 ms head start before the encrypted relay fallback begins.
- Existing agents publish no candidates and remain relay compatible. Operators can force that path with `--relay-only`.

## Automated real-process result

`npm run test:remote` passed using separate controller, agent and HTTPS relay processes. It completed a 128 MiB relayed upload, killed and resumed retrieval after restarting the relay, recovered a corrupted checkpoint, rejected a wrong device key, checked 200 small files, then restarted the same device with direct-LAN publication and verified a second 200-file retrieval selected the authenticated direct path. Revocation continued to deny fresh access.

## Physical Windows result

The existing native Windows regression suite passed on the IdeaPad: DPAPI identity reload, an 8 MiB round trip, 100 small files, an interrupted 16 MiB upload that retained 4 MiB, verified resume, and revocation.

The new Windows connectivity runner paired a clean native agent, published `192.168.100.5:17332` together with an unreachable WSL virtual-adapter candidate, and correctly selected the reachable authenticated route. An 8 MiB upload was published on Windows and retrieved with matching SHA-256. The single-run upload took 11.552 seconds and download took 6.628 seconds.

The first direct attempt was blocked by Windows Defender Firewall because the test executable was copied rather than installed. The successful runner added a uniquely named TCP 17332 rule limited to the Private profile and removed it during cleanup. A production installer therefore needs an explicit firewall-consent step and stable executable path; Mesh must not silently weaken firewall policy.

Raw consolidated evidence: [2026-09-08-connectivity-foundation.json](measurements/2026-09-08-connectivity-foundation.json).

## Remaining boundary

These runs validate the LAN-direct foundation and preserve the relay/storage behavior, but they do not implement or validate internet NAT traversal. The two physical computers were on the same Wi-Fi. A controller and relay placed on a public endpoint plus clients on independent networks are still required for the WAN baseline. Browser-native bulk transfer and offline controller authorization also remain explicit future decisions.
