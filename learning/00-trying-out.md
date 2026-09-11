## Create Order
```bash
curl -s -X POST localhost:8090/orders \
  -H 'content-type: application/json' \
  -d '{"item":"coffee","amount_cents":500}' | jq
```


## List Order
```bash
curl -s localhost:8090/orders | jq
```

## Health Checks
```bash
curl -s localhost:8090/healthz   # order-service
curl -s localhost:8081/healthz   # payment-service (exposed for direct poking)
```

## Call Payment Service (this is supposed to go through order service)
```bash
curl -s -X POST localhost:8081/payments \
  -H 'content-type: application/json' \
  -d '{"order_id":0,"amount_cents":500}' | jq
```

