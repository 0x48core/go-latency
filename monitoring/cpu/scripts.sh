# 1. Check CPU usage of Redis process
top -p $(pgreq redis-server)

# 2. Redis INFO stats
redis-cli INFO stats | grep -E "instantaneous_ops_per_sec|rejected_connections"

# 3. Check slow commands
redis-cli SLOWLOG GET 10

# 4. Monitor commands are running
redis-cli MONITOR  # careful: Very costly performance-wise

# 5. Check latency
redis-cli --latency-history -i 1