function CircuitBreaker:access(conf)
    local state = get_state(service_name)
    
    if state == OPEN then
        if now - last_failure_time < conf.open_timeout then
            -- Still open, reject
            return kong.response.exit(503, {
                message = "Service unavailable",
                retry_after = open_timeout - elapsed
            })
        else
            -- Try half-open
            set_state(service_name, HALF_OPEN)
        end
    end
end

function CircuitBreaker:header_filter(conf)
    local status = kong.response.get_status()
    local latency = ngx.now() - request_start_time
    
    local is_failure = (status >= 500) or (latency > conf.timeout_threshold)
    
    if is_failure then
        failures = increment_failures(service_name)
        
        if failures >= conf.failure_threshold then
            set_state(service_name, OPEN)
            log.warn("Circuit breaker opened", service_name)
        end
    else if state == HALF_OPEN then
        successes = increment_successes(service_name)
        
        if successes >= conf.success_threshold then
            set_state(service_name, CLOSED)
            reset_counters(service_name)
        end
    end
end