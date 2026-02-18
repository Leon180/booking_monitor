# Nginx / API Gateway

## docker-compose

```yaml
nginx:
  image: nginx:alpine
  ports:
    - "80:80"
    - "443:443"
  volumes:
    - ./deploy/nginx/nginx.conf:/etc/nginx/nginx.conf:ro
  depends_on: [api]
```

## nginx.conf (rate limiting + reverse proxy)

```nginx
http {
    # Rate limiting: 100 req/s per IP, burst of 200
    limit_req_zone $binary_remote_addr zone=booking:10m rate=100r/s;

    upstream ticket_service {
        server api:8080;
    }

    server {
        listen 80;

        location /api/ {
            limit_req zone=booking burst=200 nodelay;
            limit_req_status 429;

            proxy_pass http://ticket_service/;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }

        location /metrics {
            deny all; # Never expose Prometheus metrics publicly
        }
    }
}
```

## Key settings

| Setting | Value | Reason |
|---|---|---|
| `rate` | `100r/s` | Tune based on benchmark results |
| `burst` | `200` | Allow short spikes without 429 |
| `nodelay` | enabled | Don't queue, reject immediately |

## WAF (optional)

For production, add ModSecurity or use a managed API Gateway (AWS API GW, Kong, Traefik).
