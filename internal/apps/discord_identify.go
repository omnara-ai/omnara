package apps

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/omnara-ai/omnara/internal/apps/discord"
)

type identifyRedis interface {
	EvalInt(context.Context, string, []string, ...any) (int, error)
}

// Reserve one start because exhausting Discord's session budget can reset the token.
// A zero reset interval uses 24 hours to avoid immediately forgetting that budget.
const discordIdentifyPermitScript = `
local remaining = tonumber(redis.call('GET', KEYS[1]))
local observed = tonumber(ARGV[1])
local reset = tonumber(ARGV[2])
if reset == 0 then reset = 86400000 end
if not remaining then
  remaining = observed
  redis.call('SET', KEYS[1], remaining, 'PX', reset)
elseif observed < remaining then
  remaining = observed
  redis.call('SET', KEYS[1], remaining, 'KEEPTTL')
end
if remaining <= 1 then return math.max(redis.call('PTTL', KEYS[1]), 1000) end
local spacing = redis.call('PTTL', KEYS[2])
if spacing > 0 then return spacing end
redis.call('DECR', KEYS[1])
redis.call('SET', KEYS[2], '1', 'PX', 5000)
return 0
`

type discordIdentifyWaitError struct{ After time.Duration }

func (w discordIdentifyWaitError) Error() string {
	return "Discord session start permit is not yet available"
}

func acquireDiscordIdentify(
	ctx context.Context,
	redis identifyRedis,
	client *discord.Client,
	applicationID string,
	shard int,
) error {
	info, err := client.GetGatewayBot(ctx)
	if err != nil {
		return err
	}
	if info.SessionStartLimit.ResetAfterMillis < 0 || info.SessionStartLimit.MaxConcurrency < 1 {
		return fmt.Errorf("invalid Discord session start budget")
	}
	prefix := "omnara:discord:identify:{" + applicationID + "}:"
	keys := []string{prefix + "budget", prefix + "bucket:" + strconv.Itoa(shard%info.SessionStartLimit.MaxConcurrency)}
	waitMillis, err := redis.EvalInt(
		ctx,
		discordIdentifyPermitScript,
		keys,
		info.SessionStartLimit.Remaining,
		info.SessionStartLimit.ResetAfterMillis,
	)
	if err != nil {
		return err
	}
	if waitMillis > 0 {
		return discordIdentifyWaitError{After: time.Duration(waitMillis) * time.Millisecond}
	}
	return nil
}
