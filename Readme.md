# Code Execution Engine

NATS-queue worker that runs code in Docker sandbox containers.

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `ENVIRONMENT` | No | `development` | Runtime environment |
| `NATSURL` | No | `nats://localhost:4222` | NATS server URL |
| `MAXWORKERS` | No | `2` | Max concurrent code executions |
| `JOBCOUNT` | No | `3` | Job queue depth |
