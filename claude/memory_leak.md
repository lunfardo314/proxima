# How to diagnose memory leak issues

```bash
# Enable pprof in proxima.yaml
pprof:
  enable: true
  port: 8080

# Capture heap profiles
curl -o heap1.pprof http://localhost:8080/debug/pprof/heap
# wait 30-60 minutes
curl -o heap2.pprof http://localhost:8080/debug/pprof/heap

# Compare allocations
go tool pprof -top -diff_base=heap1.pprof heap2.pprof
```

Send diff file to Claude 
