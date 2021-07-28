package service

import (
	"context"

	"github.com/go-redis/redis/v8"
	"github.com/pkg/errors"

	"github.com/livekit/livekit-recorder/worker/pkg/config"
	"github.com/livekit/livekit-recorder/worker/pkg/logger"
)

func StartRedis(conf *config.Config) (*redis.Client, error) {
	logger.Infow("connecting to redis work queue", "addr", conf.Redis.Address)
	rc := redis.NewClient(&redis.Options{
		Addr:     conf.Redis.Address,
		Username: conf.Redis.Username,
		Password: conf.Redis.Password,
		DB:       conf.Redis.DB,
	})
	err := rc.Ping(context.Background()).Err()
	if err != nil {
		err = errors.Wrap(err, "unable to connect to redis")
	}
	return rc, err
}
