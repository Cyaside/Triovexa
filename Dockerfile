FROM node:24-alpine AS ui
WORKDIR /src
COPY . .
RUN cd ui && npm ci && npm run build

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY --from=ui /src .
RUN CGO_ENABLED=0 go build -trimpath -o /out/triovexa ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -o /out/triovexa-admin ./cmd/triovexa-admin \
 && CGO_ENABLED=0 go build -trimpath -o /out/triovexa-workload ./cmd/workload

FROM alpine:3.22
RUN addgroup -S triovexa && adduser -S -G triovexa triovexa
COPY --from=build /out/* /usr/local/bin/
COPY docs /app/docs
WORKDIR /app
USER triovexa
ENTRYPOINT ["triovexa"]
