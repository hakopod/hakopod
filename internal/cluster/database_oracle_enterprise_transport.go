package cluster

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
)

func (c *Client) verifyOracleEnterpriseTLS(ctx context.Context, d database.Resource, o *database.Observation) error {
	if len(o.Members) != d.Spec.Members() || o.Primary == "" {
		return fmt.Errorf("Oracle TCPS verification requires the complete owned topology")
	}
	config, err := c.oracleTLSConfig(ctx, d)
	if err != nil {
		return err
	}
	group, check := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for _, member := range o.Members {
		group.Go(func() error {
			step, stop := context.WithTimeout(check, 8*time.Second)
			defer stop()
			raw, err := c.oracleStream(step, d, member)
			if err != nil {
				return err
			}
			conn := tls.Client(raw, config.Clone())
			err = conn.HandshakeContext(step)
			_ = conn.Close()
			if err != nil {
				return fmt.Errorf("Oracle member has not loaded its issued TCPS identity")
			}
			// No member may expose a plaintext listener, including standbys and
			// their static Data Guard listener. Private networking is not TLS.
			command := []string{"bash", "-c", `awk 'FNR>1 && $4=="0A" && $2 ~ /:(05F1|157C)$/ {bad=1} END {exit bad}' /proc/net/tcp /proc/net/tcp6`}
			if err = c.DatabaseExec(step, d, member, command, nil, nil); err != nil {
				return fmt.Errorf("Oracle member still exposes a plaintext listener")
			}
			return nil
		})
	}
	if err = group.Wait(); err != nil {
		return err
	}
	for _, member := range o.Members {
		if member.Name != o.Primary || member.Role != "primary" {
			continue
		}
		step, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		plain, err := c.oracleApplicationConnection(step, d, member, false)
		if err != nil {
			return err
		}
		defer plain.Close()
		if plain.PingContext(step) == nil {
			return fmt.Errorf("Oracle primary accepted application credentials over plaintext")
		}
		return nil
	}
	return fmt.Errorf("Oracle TCPS verification has no unique primary")
}
