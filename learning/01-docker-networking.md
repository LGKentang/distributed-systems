`docker network ls`
NETWORK ID     NAME                          DRIVER    SCOPE
7168a245b787   bridge                        bridge    local
2d3bd90cc697   distributed-systems_default   bridge    local

`docker network inspect distributed-systems_default`

# this is after stripping away other details

"Name": "distributed-systems-postgres-1" => "IPv4Address": "172.21.0.2/16",
"Name": "distributed-systems-payment-service-1" => "IPv4Address": "172.21.0.3/16",
"Name": "distributed-systems-order-service-1" => "IPv4Address": "172.21.0.4/16",


`docker compose exec order-service` doesn't work because it is a distroless shell, so we want to create a temporary/throwaway container in this case to explore in this network.

`docker run --rm -it --network distributed-systems_default nicolaka/netshoot`

```bash
# ip route
default via 172.21.0.1 dev eth0 
172.21.0.0/16 dev eth0 proto kernel scope link src 172.21.0.5 

# ping payment-service
64 bytes from distributed-systems-payment-service-1.distributed-systems_default (172.21.0.3): icmp_seq=1 ttl=64 time=0.087 ms

# curl payment-service
{"service":"payment-service","status":"ok"}
```

- Can you show me the flow from netshoot container target the payment-service with `payment-service:8081`?
  - The low level flow is:
    1. The nsswitch.conf in the container will select what to target (ex: files `/etc/hosts`, dns `/etc/resolv.conf`)
    2. If the host can find the DNS IP, it will target that ip to request the container IP
    3. If container IP exists, then it will return code 0 (NOERROR)
    4. 
- What if the container IP didn't exist after resolving it in the DNS? What IP does the DNS return? 
  - It will return NXDOMAIN (name resolution failure)
- What is the point of the Gateway (172.21.0.1)?
- What is the difference between `/etc/hosts` and `/etc/resolv.conf`?