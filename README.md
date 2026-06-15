# distributed-systems playground

A deliberately small, deliberately *imperfect* microservice setup for learning
distributed systems, networking, observability, and (eventually) service mesh
concepts by **breaking it and fixing it yourself**.

```
  you (browser / curl)
        │  HTTP
        ▼
  order-service        :8080   ── creates orders, calls payment SYNCHRONOUSLY
        │  HTTP (no timeout, no retry)
        ▼
  payment-service      :8081   ── can be made SLOW or FLAKY on purpose
        │  SQL
        ▼
  postgres             :5432   ── shared database (intentional smell)
```

Everything is Go standard library + `lib/pq` (a pure-Go Postgres driver). No
web framework, no service mesh, no message broker, no metrics stack. Those are
things you will *add yourself* as you work through the challenges below.

---

## What's in here

```
.
├── docker-compose.yml         # the whole topology
├── db/
│   └── schema.sql             # orders + payments tables (runs on first init)
├── order-service/
│   ├── main.go                # REST API + tiny web UI, calls payment-service
│   ├── Dockerfile
│   └── go.mod / go.sum
├── payment-service/
│   ├── main.go                # REST API, configurable slowness + failure
│   ├── Dockerfile
│   └── go.mod / go.sum
└── README.md                  # you are here
```

---

## Prerequisites

- Docker + the Docker Compose plugin (`docker compose version` should work).
- That's it. You do **not** need Go installed to run it — it builds inside
  Docker. (Go 1.24 is only needed if you want to build the binaries directly.)

---

## Run it

```bash
docker compose up --build
```

First boot builds two Go images and starts Postgres. When you see
`order-service listening on :8080`, open:

- **Web UI:**   http://localhost:8090
- **API:**      see below

Stop with `Ctrl-C`, or `docker compose down` (add `-v` to also wipe the DB).

---

## Try the API

Create an order (order-service will synchronously call payment-service):

```bash
curl -s -X POST localhost:8090/orders \
  -H 'content-type: application/json' \
  -d '{"item":"coffee","amount_cents":500}' | jq
```

List orders:

```bash
curl -s localhost:8090/orders | jq
```

Health checks:

```bash
curl -s localhost:8090/healthz   # order-service
curl -s localhost:8081/healthz   # payment-service (exposed for direct poking)
```

Call payment-service **directly** (bypassing order-service):

```bash
curl -s -X POST localhost:8081/payments \
  -H 'content-type: application/json' \
  -d '{"order_id":0,"amount_cents":500}' | jq
```

### Endpoints

| Service | Method | Path | Purpose |
|---|---|---|---|
| order   | GET  | `/`         | tiny HTML UI |
| order   | POST | `/orders`   | create order → calls payment-service |
| order   | GET  | `/orders`   | list orders |
| order   | GET  | `/healthz`  | liveness + db ping |
| payment | POST | `/payments` | approve/decline a payment |
| payment | GET  | `/healthz`  | liveness + db ping |

---

## The knobs (break things on purpose)

`payment-service` reads two environment variables (set in `docker-compose.yml`):

| Var | Meaning | Try |
|---|---|---|
| `DELAY_MS`  | sleep this many ms before responding | `2000` = slow downstream |
| `FAIL_RATE` | probability `[0.0–1.0]` of a random 500 | `0.5` = flaky downstream |

Change them, then:

```bash
docker compose up -d --build payment-service   # rebuild just that service
```

Now create an order and watch order-service block (slow) or fail (flaky).
Because order-service has **no timeout and no retry**, its behaviour is fully
at the mercy of payment-service. That coupling is the whole point.

---

## Look at the logs

Plain, greppable logging to stdout:

```bash
docker compose logs -f order-service
docker compose logs -f payment-service
```

Each request prints one line including how long it took to handle.

---

## Inspect the database

```bash
docker compose exec postgres psql -U app -d app -c 'TABLE orders;'
docker compose exec postgres psql -U app -d app -c 'TABLE payments;'
```

### Resetting the database

`db/schema.sql` only runs the **first** time the Postgres volume is created.
To re-run it after editing the schema, wipe the volume:

```bash
docker compose down -v && docker compose up --build
```

---

# Learning roadmap — 12 challenges

These are **for you to implement**. The code here is the *starting* state: it
already exposes the problems each challenge asks you to solve. Each challenge
lists *where to dig* in Linux / TCP / HTTP / Docker so you understand the
mechanism, not just the fix. Nothing below is implemented for you on purpose.

> Tip: keep a scratch journal. For each challenge, write (1) what you expected,
> (2) what you observed, (3) the underlying mechanism you found.

---

### Challenge 1 — Observe Docker networking & service-to-service communication

**Goal:** see *how* `order-service` reaches `payment-service` at all.

Try:
- `docker network ls` — find the network compose created (`*_default`).
- `docker network inspect <name>` — see each container's IP and the subnet.
- `docker compose exec order-service` won't work (distroless has no shell!).
  Instead run a throwaway container *on the same network* to explore:
  `docker run --rm -it --network <name> nicolaka/netshoot` then `ip addr`,
  `ping payment-service`, `curl payment-service:8081/healthz`.

**Where to investigate:**
- Linux network namespaces (`ip netns`, `lsns -t net`) — each container gets
  its own network stack.
- The Linux **bridge** device (`ip link`, `bridge link`) and `veth` pairs that
  connect a container's namespace to the host bridge.
- `iptables -t nat -L` / `nftables` — how Docker NATs published ports.

---

### Challenge 2 — Investigate DNS resolution between containers

**Goal:** understand why the hostname `payment-service` resolves at all.

Try (from a `netshoot` container on the network):
- `cat /etc/resolv.conf` — note the nameserver `127.0.0.11`. That's Docker's
  embedded DNS server.
- `dig payment-service` / `nslookup payment-service` — see the answer.
- `getent hosts payment-service`.

**Where to investigate:**
- Docker's embedded DNS at `127.0.0.11` and how it maps service names →
  container IPs on user-defined networks (and *not* on the default bridge).
- The libc resolver order: `/etc/nsswitch.conf`, `/etc/hosts`, then DNS.
- What happens when a service has multiple replicas (`docker compose up
  --scale payment-service=3`) — DNS round-robin (a preview of Challenge 9).

---

### Challenge 3 — Simulate slow downstream services

**Goal:** feel synchronous coupling.

Set `DELAY_MS: "3000"` on payment-service, rebuild, create an order, and time
it: `time curl -X POST localhost:8090/orders -d '{"item":"x","amount_cents":1}'`.
Open the web UI in two tabs and submit at once.

**Where to investigate:**
- Goroutines: Go serves each request in its own goroutine, so order-service
  isn't *single*-threaded — but each individual user still waits the full delay.
- `time.Sleep` in `payment-service/main.go` is the injected latency.
- The TCP connection stays **established** the whole time the caller waits —
  watch with `ss -tnp` inside a netshoot container or on the host.

---

### Challenge 4 — Implement request timeouts

**Goal:** stop order-service from waiting forever.

Right now `callPayment` uses `http.DefaultClient`, which has **no timeout**.
Give it a bound. Look at: `http.Client{Timeout: ...}`, and more granular control
with `context.WithTimeout` + `http.NewRequestWithContext`. Decide what status
order-service should return when the deadline is exceeded.

**Where to investigate:**
- The difference between a connect timeout, a TLS timeout, a response-header
  timeout, and a whole-request timeout (`net/http.Transport` fields).
- TCP-level timeouts vs. application timeouts: `SO_RCVTIMEO`, TCP keepalive,
  and why a dead peer can hang far longer than you'd expect without them.
- Server side: `http.Server{ReadTimeout, WriteTimeout, IdleTimeout}` — both
  services currently set none.

---

### Challenge 5 — Implement retries

**Goal:** survive *transient* failures from the flaky downstream.

Set `FAIL_RATE: "0.5"`. Wrap the payment call in a retry loop. Then confront
the hard parts:
- **Idempotency:** retrying a payment could double-charge. What makes a request
  safe to retry? (Idempotency keys, dedup in payment-service.)
- **Backoff & jitter:** why fixed-interval retries cause thundering herds.
- **Retry only the right things:** retry on 5xx/timeouts, *not* on 4xx.

**Where to investigate:**
- HTTP method semantics & idempotency (RFC 9110).
- Exponential backoff with jitter (AWS "exponential backoff and jitter").
- The interaction between timeouts (Ch.4) and retries — a retry budget.

---

### Challenge 6 — Measure latency

**Goal:** quantify what you've been feeling.

Add timing around the payment call and log p50/p95-ish numbers, or drive load
with a tool and read its histogram: `hey`, `wrk`, `vegeta`, or
`ab -n 500 -c 20 -p body.json -T application/json localhost:8090/orders`.

**Where to investigate:**
- Latency vs. throughput; why averages lie and tail latency (p99) matters.
- Little's Law (concurrency = arrival rate × latency).
- `curl -w` with a `-w '%{time_connect} %{time_starttransfer} %{time_total}\n'`
  format to break a single request into phases.

---

### Challenge 7 — Add Prometheus metrics

**Goal:** make latency/error rate observable without reading logs.

Expose a `/metrics` endpoint (counter of orders, histogram of payment latency,
counter of failures). Note the constraints said *don't add it yet* — this is
where you opt in. Use `prometheus/client_golang`, scrape with a Prometheus
container, optionally graph in Grafana.

**Where to investigate:**
- The four "golden signals" (latency, traffic, errors, saturation).
- Counter vs. gauge vs. histogram vs. summary; why histograms enable
  server-side percentiles.
- Pull-based scraping vs. push, and the `/metrics` text exposition format.

---

### Challenge 8 — Add distributed tracing

**Goal:** follow a single order across both services.

Propagate a trace/correlation ID from order-service → payment-service (start
with a simple `X-Request-Id` header you log in both, then graduate to W3C
`traceparent` + OpenTelemetry + a Jaeger/Tempo backend).

**Where to investigate:**
- Context propagation: HTTP headers carry the trace context across the network
  boundary; `context.Context` carries it *within* a process.
- Spans, parent/child relationships, and sampling.
- Why logs, metrics, and traces are "three pillars" that answer different
  questions.

---

### Challenge 9 — Introduce load balancing

**Goal:** run multiple payment-service replicas and spread traffic.

`docker compose up --scale payment-service=3`. Observe that Docker DNS already
returns multiple A records (round-robin). Then make it deliberate: put a
reverse proxy (nginx/HAProxy/Caddy) or a client-side balancer in front.

**Where to investigate:**
- L4 (TCP) vs. L7 (HTTP) load balancing.
- DNS round-robin and its caching pitfalls (clients pinning one IP).
- Health checking & connection draining; how a balancer removes a bad replica.
- HTTP keep-alive vs. balancing: a pooled connection sticks to one backend.

---

### Challenge 10 — Add a message queue

**Goal:** decouple order from payment — make the call **asynchronous**.

Introduce a broker (NATS/RabbitMQ/Kafka/Redis Streams). order-service publishes
an `order.created` event and returns immediately; payment-service consumes and
processes; order status is updated later.

**Where to investigate:**
- Synchronous request/response vs. asynchronous event-driven.
- At-least-once vs. exactly-once delivery; consumer idempotency (echoes Ch.5).
- Backpressure and queue depth as a saturation signal (echoes Ch.7).
- The new failure modes you *traded for*: eventual consistency, ordering,
  poison messages, dead-letter queues.

---

### Challenge 11 — Introduce Envoy

**Goal:** move cross-cutting concerns *out* of your Go code.

Put an Envoy sidecar in front of payment-service. Re-implement, in Envoy config
rather than code: timeouts (Ch.4), retries (Ch.5), load balancing (Ch.9),
and metrics (Ch.7).

**Where to investigate:**
- Envoy listeners, clusters, routes, and filters.
- The "sidecar" pattern: a proxy in the same network namespace as the app
  (`localhost` between app and sidecar — relate back to Ch.1 namespaces).
- How Envoy gets traffic: iptables redirection vs. explicit proxy config.

---

### Challenge 12 — Understand how a service mesh solves these problems

**Goal:** connect the dots.

Stand up a mesh (Linkerd is the gentlest; Istio is the heavyweight) and watch
it inject Envoy-like sidecars automatically. Map each earlier challenge onto a
mesh feature: mTLS, retries, timeouts, traffic splitting, golden-signal
dashboards, distributed tracing — all *without changing app code*.

**Where to investigate:**
- Control plane vs. data plane.
- Why the mesh is "just" automated sidecars + a control plane to configure them
  — everything you hand-built in Ch.4–11, centralized and declarative.
- The costs: extra hops/latency, operational complexity, another set of things
  that can break. When is a mesh worth it?

---

## A note on the "smells" left in on purpose

This playground intentionally ships with problems so you can discover them:

- **No timeouts / retries / circuit breakers** around the synchronous call.
- **Shared database** between two services (coupling).
- **`depends_on` doesn't guarantee readiness** — only start order; the app-level
  Postgres retry loop papers over it.
- **No graceful shutdown** — `Ctrl-C` can drop in-flight requests.
- **Default `http.Server` has no timeouts** — slow-client / slowloris exposure.
- **Distroless images have no shell** — great for prod, surprising when you try
  to `docker exec` in to debug (use `nicolaka/netshoot` instead).

Finding and fixing these *is* the curriculum. Have fun.
