# Interview prep

Every question from the eight phases, then the design choices written as questions. Answers are short enough
to say out loud. Numbers come from `benchmarks.md`.

## Part 1: Phase questions

### Phase 1: protocol, schema, generator
**1. How do you dedup when the same packet arrives many times?**
There are two keys. `packet_hash` comes from the firmware and covers the payload but not the path, so it joins every copy of
one transmission. `reception_id` hashes the observer, the timestamp and the raw bytes. A redelivered message gets the same id,
so writes are idempotent. Two genuine receptions get different ids.

**2. How do you know your topology inference is right?**
The synthetic generator follows the firmware's real forwarding rules and writes down the true network. I score inference
against that known answer before it ever sees real data.

**3. Why did you fuzz the decoder?**
It reads bytes from a public radio network, so it's a trust boundary. The fuzz test checks that no input crashes it and that
anything it decodes re-encodes to the same bytes. It passed 10 million inputs.

### Phase 2: ingester
**4. What happens if storage goes down?**
At first, rows stayed in memory, retried every 10 s, and readiness reported not-ready. Since Phase 7 they spool to disk
instead, so an outage loses nothing. File names come from the content, so a retry can't create a duplicate.

**5. How do you publish real network data without exposing people?**
The anonymizer remaps keys with a secret permutation that keeps hash collisions intact, so the topology problem stays honest.
It replaces names, shifts coordinates and snaps them to about 1 km, and replaces ciphertext with random bytes. The key is
random per run and never stored, and raw captures are git-ignored.

**6. Why do liveness and readiness differ?**
If the broker is down, restarting the pod fixes nothing; it just loops. Liveness asks "is the process alive?" Readiness asks
"is it connected and working?"

### Phase 3: topology and resilience
**7. Short hashes collide. How do you know which node a hop really is?**
I never treat a hop hash as an identity. Each path is resolved over the candidate nodes, weighted by how plausible each
distance is, and every link gets a confidence. On the 1-byte test data, collisions cost only 0.6% precision.

**8. Why does your recall look low?**
Companions don't relay, so links between two companions never appear in any path. Recall on links that touch a relay is
97–100%. The low number measures what the data can't contain.

**9. How do you stop AI-written code from going wrong?**
Each agent owns one part of the repo, and the parts meet only at two checked contracts: the schema and the API JSON. A
separate verifier with no edit rights must pass every change. Hooks enforce the rules that must never be skipped.

### Phase 4: terrain propagation
**10. Your model is off by 15 dB. Is it broken?**
No. The synthetic generator uses a deliberately different loss model, and I didn't tune to it. I checked each half
separately: the measured side matches ground truth to 0.06 dB, and the predicted side matches textbook values.

**11. Why knife-edge and not a full model like Longley-Rice?**
It's about 20 lines I can derive on a whiteboard, it's cited to an ITU equation, and it runs at about 6,000 paths per
second. It sits behind one interface, so a better model can replace it.

**12. How do you keep homes private when you publish distances?**
Distances are measured between the published grid cells, not the real positions, so they reveal nothing the map doesn't
already show. A test recomputes every one.

### Phase 5: placement and API
**13. Analytics already snaps coordinates. Why validate again in the API?**
Defence in depth. The API is the last stop before data leaves, and a single analytics bug could publish home locations. The
API checks against an allow-list, never serves the raw file, and fails closed.

**14. How does placement decide where a repeater goes?**
It tries a hypothetical repeater in every 1 km cell and predicts which nodes it would reach. It then counts the node pairs
that gain a route and the single points of failure removed, and the best cells win.

**15. Why plain JavaScript and not React?**
The map is about 200 lines with no build step and nothing to keep patched, and it's embedded in the Go binary.

### Phase 6: Kubernetes
**16. How do you know your batch jobs are actually running?**
Each summary carries the time it was generated. The API exports that time as a metric, but only for summaries that pass
validation. An alert fires when a summary is older than twice its schedule.

**17. Walk me through securing a pod.**
- Non-root user, read-only filesystem, all Linux capabilities dropped, and no privilege escalation.
- Memory limits set from measured peaks.
- Secrets only in Kubernetes Secrets.
- A network rule in front of the storage, which holds raw locations.

**18. Why not MinIO?**
MinIO stopped publishing community images in 2025. SeaweedFS has an official image, and the code only speaks plain S3, so
the switch changed only deployment files.

### Phase 7: DDIL
**19. What does "zero data loss" mean in your system?**
It's scoped. A storage outage, a flapping link or a graceful restart loses nothing, proven end to end at 4,337 of 4,337
receptions. A hard kill loses rows still in memory. I state exactly what was tested.

**20. Why upload newest first and evict oldest first?**
For a live twin, recent data is worth the most. Analytics gets fresh data first, and if the disk fills, old data goes and an
alert fires.

**21. How do you get exactly-once with MQTT?**
You don't. You get at-least-once and make it harmless: files are content-addressed and every reception has a deterministic
id, so duplicates are dropped when the data is read.

### Phase 8: CI and security
**22. Why pin GitHub Actions by commit hash?**
A tag can be moved to point at malicious code, as happened with tj-actions in 2025. A commit hash can't change.

**23. The scanner shows 98 HIGH findings and CI passes. Why?**
None of them has a fix yet, so the gate fails only on what we can act on. The SBOM keeps the full list, so we know what to
rebuild when fixes ship.

**24. govulncheck said x/net was safe and Trivy said it wasn't. Who was right?**
Both. govulncheck checks whether our code calls the vulnerable function; Trivy checks the version. I bumped it anyway, since
it cost one line.

## Part 2: Design choices as questions

### Languages and tools
**Why Go for the ingester and API?**
Each builds to one static binary, so the images are tiny: 29 MB and 22 MB on distroless. Goroutines suit "listen to MQTT and
flush files at the same time". The standard library covers HTTP, logging and contexts, and Go is what most cloud
infrastructure is written in.

**Why Python for analytics?**
The math libraries live there: DuckDB, networkx and numpy. Analysis changes often, and Python is quicker to iterate. Strict
mypy and tests keep it as disciplined as the Go code.

**Why two languages instead of one?**
Each side does what it's best at: Go for the always-on service that has to be fast and small, Python for analysis. They meet
only through the Parquet schema, so either side can change without breaking the other.

**Why DuckDB and not Spark or Postgres?**
The data fits on one machine: a busy region is about 120 MB of Parquet a day. DuckDB runs SQL straight on Parquet in S3,
inside the job, with no server to run. Spark would be a cluster for a laptop-sized problem.

**Why networkx?**
Articulation points, bridges and reachability are already implemented and well tested, and the graph has hundreds of nodes,
not millions.

**Why uv?**
It's fast, and the lockfile makes builds reproducible: CI fails if the lockfile is stale.

### Data and storage
**Why Parquet?**
It's columnar and compressed: 21.8× smaller than the raw JSON. Analytics reads only the columns it needs, and both Go and
Python support it well.

**Why object storage (S3) and not a database?**
The data is write-once and append-only, which is exactly what object storage is for. It's cheap, and partitioning by
region/date/hour lets a query skip everything outside its time window.

**Why SeaweedFS?**
An official image from its maintainer, small, and S3-compatible. Because the code only speaks plain S3, it could be swapped
for AWS S3 or anything else without code changes.

**Why decode the raw bytes and not trust the observer's JSON fields?**
Different observer tools format the JSON inconsistently. The raw packet is the source of truth, and I decode it the way the
firmware's own code does.

**Why a synthetic data generator?**
You can't score inference without knowing the right answer, and real networks don't publish theirs. The generator gives a
known truth. It's used only for tests, CI and demos, always labelled.

**Why Seattle?**
One region keeps the work focused, and it has an active MeshCore community and real terrain to test against.

### Messaging and resilience
**Why MQTT?**
It's what MeshCore observers already publish over. I fit the existing ecosystem rather than inventing a feed.

**Why not Kafka?**
It's another stateful cluster to run. The MQTT broker already buffers for my own observer, and the disk spool covers storage
outages. With one ingester, Kafka adds cost without solving a problem I have.

**Why two settings, own observer and public broker?**
Different owners deserve different behaviour. On my broker I use QoS 1 so it holds messages during restarts. On a community
broker I use QoS 0, subscribe only and rate-limited, because asking someone else's broker to queue for me isn't polite.

**Why content-addressed file names?**
A retry writes the same name with the same content, so uploading twice is harmless. That's what makes retries and spool
drains safe.

**Why a disk spool rather than a bigger memory buffer?**
Memory dies with the pod, while a persistent volume survives restarts. That was proven when the ingester pod was killed
mid-outage and its replacement found the old spool.

### Analysis method
**Why the knife-edge model and Copernicus GLO-30 terrain?**
The model is simple, cited and fast. Copernicus GLO-30 covers the whole world at 30 m with a licence that allows this use.
Known limit: it includes trees and roofs, so it over-predicts blockage in forests.

**Why a ~1 km grid for locations?**
Fine enough to plan where a repeater goes, coarse enough that a dot isn't a house. It's applied at every output, and the
API checks it again.

**Why confidence scores on links?**
Because hop hashes collide, every inferred link is a guess. A confidence lets users filter, and lets resilience say how sure
it is.

### Platform
**Why k3d?**
It runs real k3s, the same distribution used on edge devices, inside Docker. A cluster starts in about 30 s. k3s's edge
focus fits the disconnected-operations (DDIL) theme.

**Why Helm?**
One chart with different values files for real use, smoke tests and chaos tests. Secrets are generated once and kept across
upgrades.

**Why CronJobs instead of a long-running analytics service?**
Analytics is periodic batch work. CronJobs give scheduling, retries and no overlapping runs for free, and use no resources
between runs.

**Why a read-only API?**
Nothing can be written through it, so there's far less to attack, and it can scale to any number of replicas with no shared
state.

**Why distroless images?**
No shell and no package manager, so there are fewer vulnerabilities and less for an attacker to use. It runs non-root by
default.

**Why Prometheus and Grafana?**
They're the Kubernetes standard. Pull-based metrics fit pods that come and go, and the alerts live as code in the repo.

**Why an SBOM?**
It's the exact list of what shipped. When a new vulnerability is announced, you check the list instead of rebuilding
everything, and US government supply-chain rules (EO 14028) ask for one.

**Why build with AI agents and a verifier?**
Speed with accountability: agents write the code, an independent checker has to pass every change, and I review and own
each decision. That's why every answer above is one I can defend.
