# Critical Alerts to Set Up

## Alert: RDB save failing

STATUS=$(redis-cli INFO persistence | grep rdb_last_bgsave_status | cut -d: -f2 | tr -d '\r ')
if [ "$STATUS" != "ok" ]; then
    echo "CRITICAL: Redis RDB save failed - status: $STATUS"
fi

## Alert: No save in a long time

LAST_SAVE=$(redis-cli LASTSAVE)
NOW=$(date +%s)
AGE=$((NOW - LAST_SAVE))
if [ "$AGE" -gt 7200 ]; then
    echo "WARNING: Redis has not saved in $((AGE/3600)) hours"
fi

## Alert: High AOF delayed fsync

DELAYED=$(redis-cli INFO persistence | grep aof_delayed_fsync | cut -d: -f2 | tr -d '\r ')
if [ "$DELAYED" -gt 100 ]; then
    echo "WARNING: AOF delayed fsync count: $DELAYED"
fi

## Alert: Large changes since last save

CHANGES=$(redis-cli INFO persistence | grep rdb_changes_since_last_save | cut -d: -f2 | tr -d '\r ')
if [ "$CHANGES" -gt 1000000 ]; then
    echo "WARNING: $CHANGES unsaved changes since last RDB snapshot"
fi