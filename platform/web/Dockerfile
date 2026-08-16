FROM node:20-alpine AS builder

WORKDIR /app

COPY package.json package-lock.json* ./

RUN npm ci || npm install

COPY . .

# Vite inlines import.meta.env at build time, so these have to arrive as build
# args. Supplying them as container environment does nothing: by then the bundle
# is already written and VITE_OIDC_ISSUER is undefined, which leaves
# src/auth/config.ts with an empty authority and sign-in permanently broken.
ARG VITE_API_URL=/api/v1
ARG VITE_OIDC_ISSUER
ARG VITE_OIDC_CLIENT_ID=odv-portal

ENV VITE_API_URL=$VITE_API_URL \
    VITE_OIDC_ISSUER=$VITE_OIDC_ISSUER \
    VITE_OIDC_CLIENT_ID=$VITE_OIDC_CLIENT_ID

# Fail the build rather than shipping a portal that cannot reach an issuer.
RUN test -n "$VITE_OIDC_ISSUER" || { \
        echo "VITE_OIDC_ISSUER build arg is required; the portal cannot sign anyone in without it" >&2; \
        exit 1; \
    }

RUN npm run build

FROM nginx:alpine

COPY --from=builder /app/dist /usr/share/nginx/html

RUN rm /etc/nginx/conf.d/default.conf

COPY <<'EOF' /etc/nginx/conf.d/default.conf
server {
    listen 80;
    server_name _;

    root /usr/share/nginx/html;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
    }

    location /api/ {
        proxy_pass http://api:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
EOF

EXPOSE 80

# 127.0.0.1 rather than localhost: busybox wget resolves localhost to ::1 first
# and nginx's `listen 80` binds IPv4 only, so the check would always be refused
# and the container would never report healthy.
HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://127.0.0.1:80/ || exit 1

CMD ["nginx", "-g", "daemon off;"]
