package portforward

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/assert"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
)

func init() {
	logf.SetLogger(logf.ZapLogger(true))
}

type capturingDialer struct {
	addresses []string
}

func (d *capturingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.addresses = append(d.addresses, address)
	return nil, nil
}

func NewPodForwarderWithTest(t *testing.T, network, addr string) *podForwarder {
	fwd, err := NewPodForwarder(network, addr)
	require.NoError(t, err)
	return fwd
}

type stubPortForwarder struct {
	ctx context.Context
}

func (c *stubPortForwarder) ForwardPorts() error {
	<-c.ctx.Done()
	return nil
}

func Test_podForwarder_DialContext(t *testing.T) {
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name         string
		forwarder    *podForwarder
		tweaks       func(t *testing.T, f *podForwarder)
		args         args
		wantDialArgs []string
		wantErr      bool
	}{
		{
			name:      "pod should be forwarded",
			forwarder: NewPodForwarderWithTest(t, "tcp", "foo.bar.pod.cluster.local:9200"),
			tweaks: func(t *testing.T, f *podForwarder) {
				f.ephemeralPortFinder = func() (string, error) {
					return "12345", nil
				}
				f.portForwarderFactory = PortForwarderFactory(func(
					ctx context.Context,
					namespace, podName string,
					ports []string,
					readyChan chan struct{},
				) (PortForwarder, error) {
					assert.Equal(t, "bar", namespace)
					assert.Equal(t, "foo", podName)
					assert.Equal(t, []string{"12345:9200"}, ports)

					// closing the readyChan to pretend we're ready
					close(readyChan)

					return &stubPortForwarder{ctx: ctx}, nil
				})
			},
			wantDialArgs: []string{"127.0.0.1:12345"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialer := &capturingDialer{}
			tt.forwarder.dialerFunc = dialer.DialContext

			if tt.args.ctx == nil {
				tt.args.ctx = context.TODO()
			}

			// wait for our goroutines to finish before returning
			wg := sync.WaitGroup{}
			defer wg.Wait()

			ctx, canceller := context.WithTimeout(tt.args.ctx, 5*time.Second)
			defer canceller()

			if tt.tweaks != nil {
				tt.tweaks(t, tt.forwarder)
			}

			wg.Add(1)
			currentTest := tt
			go func() {
				defer wg.Done()
				err := currentTest.forwarder.Run(ctx)
				if !currentTest.wantErr {
					assert.NoError(t, err)
				}
			}()

			_, err := tt.forwarder.DialContext(ctx)

			if (err != nil) != tt.wantErr {
				t.Errorf("podForwarder.DialContext() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			assert.Equal(t, tt.wantDialArgs, dialer.addresses)
		})
	}
}

func Test_parsePodAddr(t *testing.T) {
	type args struct {
		addr string
	}
	tests := []struct {
		name    string
		args    args
		want    types.NamespacedName
		wantErr error
	}{
		{
			name: "without subdomain",
			args: args{addr: "foo.bar.pod.cluster.local"},
			want: types.NamespacedName{Namespace: "bar", Name: "foo"},
		},
		{
			name:    "invalid",
			args:    args{addr: "example.com"},
			wantErr: errors.New("unsupported pod address format: example.com"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePodAddr(tt.args.addr)

			if tt.wantErr != nil {
				assert.Equal(t, tt.wantErr, err)
				return
			}
			assert.NoError(t, err)

			assert.Equal(t, tt.want, *got)
		})
	}
}