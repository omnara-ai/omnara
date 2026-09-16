// All keys share the physical application's Redis Cluster hash tag. Redis TIME
// avoids comparing clocks from different gateway hosts. No token is stored here.
const clock = `local t=redis.call('TIME'); local now=t[1]*1000+math.floor(t[2]/1000);`

export const readIdentifyState =
  clock +
  `
  if redis.call('EXISTS',KEYS[2])==1 then return {'wait',tostring(redis.call('PTTL',KEYS[2]))} end
  local raw=redis.call('GET',KEYS[1]); if not raw then return {'refresh'} end
  local s=cjson.decode(raw)
  if now>=s.cachedUntil or (s.resetAt>0 and now>=s.resetAt) then return {'refresh'} end
  return {'ok',raw}
`

export const commitIdentifyState =
  clock +
  `
  if redis.call('GET',KEYS[2])~=ARGV[1] then return 0 end
  local info=cjson.decode(ARGV[2]); local limit=info.session_start_limit
  local remaining=limit.remaining; local resetAt=0
  if limit.reset_after>0 then resetAt=now+limit.reset_after end
  local old=redis.call('GET',KEYS[1])
  if old then
    old=cjson.decode(old)
    if old.resetAt==0 or now<old.resetAt then
      remaining=math.min(remaining,old.remaining)
      resetAt=math.max(resetAt,old.resetAt)
    end
  end
  local s={info=info,remaining=remaining,resetAt=resetAt,cachedUntil=now+tonumber(ARGV[3])}
  redis.call('SET',KEYS[1],cjson.encode(s))
  redis.call('DEL',KEYS[2]); return 1
`

export const reserveIdentify =
  clock +
  `
  if redis.call('EXISTS',KEYS[2])==1 then return {'wait',tostring(redis.call('PTTL',KEYS[2]))} end
  local raw=redis.call('GET',KEYS[1]); if not raw then return {'refresh'} end
  local s=cjson.decode(raw)
  if now>=s.cachedUntil or (s.resetAt>0 and now>=s.resetAt) then return {'refresh'} end
  if s.info.session_start_limit.max_concurrency~=tonumber(ARGV[1]) then return {'refresh'} end
  -- Check the entire earlier prefix: cached READY facts may have been recorded
  -- with a different max_concurrency. BITPOS scans bytes, not one RPC per shard.
  -- A cached own READY also survives a crash before the next core checkpoint.
  if ARGV[5]~='1' and redis.call('GETBIT',KEYS[4],ARGV[4])==0 then
    local groupStart=math.floor(tonumber(ARGV[4])/tonumber(ARGV[1]))*tonumber(ARGV[1])
    local missing=redis.call('BITPOS',KEYS[4],0)
    if missing>=0 and missing<groupStart then return {'startup_deferred'} end
  end
  if s.remaining<=0 then return {'exhausted'} end
  if not redis.call('SET',KEYS[3],ARGV[2],'NX','PX',ARGV[3]) then
    return {'wait',tostring(math.max(1,redis.call('PTTL',KEYS[3])))}
  end
  s.remaining=s.remaining-1
  redis.call('SET',KEYS[1],cjson.encode(s))
  return {'granted'}
`

export const unlockIdentifyRefresh = `
  if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end
  return 0
`
