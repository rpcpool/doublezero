package bgp

import (
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/jwhited/corebgp"
	"github.com/malbeclabs/doublezero/client/doublezerod/internal/liveness"
	"github.com/malbeclabs/doublezero/client/doublezerod/internal/routing"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const testIBRLTable = 120

func flushTestRoute(dst string, nextHop net.IP, table int) *routing.Route {
	return &routing.Route{
		Src:      net.IPv4(7, 7, 7, 7),
		Dst:      &net.IPNet{IP: net.ParseIP(dst).To4(), Mask: net.CIDRMask(32, 32)},
		NextHop:  nextHop,
		Table:    table,
		Protocol: unix.RTPROT_BGP,
	}
}

func requireDownEvent(t *testing.T, ch chan SessionEvent) {
	t.Helper()
	select {
	case ev := <-ch:
		require.Equal(t, SessionStatusDown, ev.Session.SessionStatus)
	default:
		require.Fail(t, "expected a SessionEvent on PeerStatusChan")
	}
}

func TestClient_BGPPlugin_OnCloseCustomTableFlushesOnlyThatTable(t *testing.T) {
	t.Parallel()

	peerIP := net.ParseIP("203.0.113.1")
	otherPeerIP := net.ParseIP("203.0.113.2")

	var deleted []*routing.Route
	var mainListed bool
	mockRW := &MockRouteReaderWriter{
		// The main-table listing never sees a custom-table route.
		RouteByProtocolFunc: func(int) ([]*routing.Route, error) {
			mainListed = true
			return nil, nil
		},
		RouteByProtocolAllTablesFunc: func(p int) ([]*routing.Route, error) {
			require.Equal(t, unix.RTPROT_BGP, p)
			return []*routing.Route{
				flushTestRoute("1.1.1.1", peerIP, testIBRLTable),
				flushTestRoute("2.2.2.2", otherPeerIP, testIBRLTable),
				flushTestRoute("3.3.3.3", peerIP, 100),
				flushTestRoute("4.4.4.4", peerIP, unix.RT_TABLE_MAIN),
			}, nil
		},
		RouteDeleteFunc: func(r *routing.Route) error {
			deleted = append(deleted, r)
			return nil
		},
	}

	peerStatus := make(chan SessionEvent, 1)
	p := &Plugin{PeerStatusChan: peerStatus, RouteTable: testIBRLTable, RouteReaderWriter: mockRW}
	p.MarkDeleted()
	p.OnClose(corebgp.PeerConfig{RemoteAddress: netip.MustParseAddr("203.0.113.1")})

	requireDownEvent(t, peerStatus)
	require.False(t, mainListed, "a custom table must not use the main-table listing")
	require.Len(t, deleted, 1, "only routes in the IBRL table via the closed peer go")
	require.Equal(t, "1.1.1.1/32", deleted[0].Dst.String())
	require.Equal(t, testIBRLTable, deleted[0].Table)
	require.Len(t, mockRW.forgottenVia, 1)
	require.True(t, mockRW.forgottenVia[0].Equal(peerIP))
}

func TestClient_BGPPlugin_OnCloseMainTableKeepsMainListing(t *testing.T) {
	t.Parallel()

	peerIP := net.ParseIP("203.0.113.1")
	for _, table := range []int{0, unix.RT_TABLE_MAIN} {
		var deleted []*routing.Route
		mockRW := &MockRouteReaderWriter{
			RouteByProtocolFunc: func(int) ([]*routing.Route, error) {
				return []*routing.Route{flushTestRoute("1.1.1.1", peerIP, unix.RT_TABLE_MAIN)}, nil
			},
			RouteByProtocolAllTablesFunc: func(int) ([]*routing.Route, error) {
				t.Errorf("table %d: the main table must not use the all-tables listing", table)
				return nil, nil
			},
			RouteDeleteFunc: func(r *routing.Route) error {
				deleted = append(deleted, r)
				return nil
			},
		}

		peerStatus := make(chan SessionEvent, 1)
		p := &Plugin{PeerStatusChan: peerStatus, RouteTable: table, RouteReaderWriter: mockRW}
		p.MarkDeleted()
		p.OnClose(corebgp.PeerConfig{RemoteAddress: netip.MustParseAddr("203.0.113.1")})

		requireDownEvent(t, peerStatus)
		require.Len(t, deleted, 1, "table %d", table)
		require.Equal(t, "1.1.1.1/32", deleted[0].Dst.String())
	}
}

// mainOnlyRouteReaderWriter cannot list routes outside the main table.
type mainOnlyRouteReaderWriter struct{ deleted int }

func (m *mainOnlyRouteReaderWriter) RouteAdd(*routing.Route) error { return nil }
func (m *mainOnlyRouteReaderWriter) RouteDelete(*routing.Route) error {
	m.deleted++
	return nil
}
func (m *mainOnlyRouteReaderWriter) RouteByProtocol(int) ([]*routing.Route, error) {
	return []*routing.Route{flushTestRoute("1.1.1.1", net.ParseIP("203.0.113.1"), unix.RT_TABLE_MAIN)}, nil
}

func TestClient_BGPPlugin_OnCloseCustomTableWithoutAllTablesListerDeletesNothing(t *testing.T) {
	t.Parallel()

	rw := &mainOnlyRouteReaderWriter{}
	peerStatus := make(chan SessionEvent, 1)
	p := &Plugin{PeerStatusChan: peerStatus, RouteTable: testIBRLTable, RouteReaderWriter: rw}
	p.MarkDeleted()
	p.OnClose(corebgp.PeerConfig{RemoteAddress: netip.MustParseAddr("203.0.113.1")})

	requireDownEvent(t, peerStatus)
	require.Zero(t, rw.deleted, "a main-table route must never be deleted for a custom-table plugin")
}

func TestClient_RouteReaderWriterWithNoUninstall_ForwardsAllTablesListing(t *testing.T) {
	t.Parallel()

	want := []*routing.Route{flushTestRoute("1.1.1.1", net.ParseIP("203.0.113.1"), testIBRLTable)}
	wrapped := newRouteReaderWriterWithNoUninstall(&MockRouteReaderWriter{
		RouteByProtocolAllTablesFunc: func(int) ([]*routing.Route, error) { return want, nil },
	}, false)
	got, err := routing.RouteByProtocolAllTables(wrapped, unix.RTPROT_BGP)
	require.NoError(t, err)
	require.Equal(t, want, got)

	_, err = routing.RouteByProtocolAllTables(newRouteReaderWriterWithNoUninstall(&mainOnlyRouteReaderWriter{}, false), unix.RTPROT_BGP)
	require.Error(t, err)
}

// kernelRecorder stands in for the netlink backend under the liveness manager.
type kernelRecorder struct {
	mu      sync.Mutex
	deleted []*routing.Route
}

func (k *kernelRecorder) RouteAdd(*routing.Route) error { return nil }
func (k *kernelRecorder) RouteDelete(r *routing.Route) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deleted = append(k.deleted, r)
	return nil
}
func (k *kernelRecorder) RouteByProtocol(int) ([]*routing.Route, error) { return nil, nil }

func TestClient_BGPPlugin_OnCloseCustomTableWithPassiveLivenessDeletesKernelRoute(t *testing.T) {
	t.Parallel()

	kernel := &kernelRecorder{}
	lm, err := liveness.NewManager(t.Context(), &liveness.ManagerConfig{
		Logger:          slog.Default(),
		Netlinker:       kernel,
		MetricsRegistry: prometheus.NewRegistry(),
		BindIP:          "127.0.0.1",
		TxMin:           100 * time.Millisecond,
		RxMin:           100 * time.Millisecond,
		DetectMult:      3,
		MinTxFloor:      50 * time.Millisecond,
		MaxTxCeil:       time.Second,
		BackoffMax:      time.Second,
		ClientVersion:   "1.2.3-dev",
		PassiveMode:     true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lm.Close() })

	peerIP := net.ParseIP("127.0.0.1")
	rrw := liveness.NewRouteReaderWriter(lm, kernel, "lo", false)
	require.NoError(t, rrw.RouteAdd(flushTestRoute("127.0.0.2", peerIP, testIBRLTable)))

	peerStatus := make(chan SessionEvent, 1)
	p := &Plugin{PeerStatusChan: peerStatus, RouteTable: testIBRLTable, RouteReaderWriter: rrw}
	p.MarkDeleted()
	p.OnClose(corebgp.PeerConfig{RemoteAddress: netip.MustParseAddr("127.0.0.1")})

	requireDownEvent(t, peerStatus)
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	require.Len(t, kernel.deleted, 1)
	require.Equal(t, "127.0.0.2/32", kernel.deleted[0].Dst.String())
	require.Equal(t, testIBRLTable, kernel.deleted[0].Table)
	require.Empty(t, lm.GetSessions())
}
