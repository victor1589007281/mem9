FROM ubuntu:22.04
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY bin/mnemo-server /mnemo-server
EXPOSE 8080
ENTRYPOINT ["/mnemo-server"]
