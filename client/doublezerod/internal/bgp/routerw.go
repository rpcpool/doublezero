package bgp

import (
	"net"

	"github.com/malbeclabs/doublezero/client/doublezerod/internal/routing"
)

var _ routing.AllTablesRouteLister = (*routeReaderWriterWithNoUninstall)(nil)

type routeReaderWriterWithNoUninstall struct {
	routeReaderWriter RouteReaderWriter
	noUninstall       bool
}

func newRouteReaderWriterWithNoUninstall(routeReaderWriter RouteReaderWriter, noUninstall bool) RouteReaderWriter {
	return &routeReaderWriterWithNoUninstall{
		routeReaderWriter: routeReaderWriter,
		noUninstall:       noUninstall,
	}
}

func (r *routeReaderWriterWithNoUninstall) RouteAdd(route *routing.Route) error {
	return r.routeReaderWriter.RouteAdd(route)
}

func (r *routeReaderWriterWithNoUninstall) RouteDelete(route *routing.Route) error {
	if r.noUninstall {
		// The kernel route stays, but BGP withdrew it, so the route reconciler
		// must stop restoring it.
		routing.ForgetRoute(r.routeReaderWriter, route)
		return nil
	}
	return r.routeReaderWriter.RouteDelete(route)
}

func (r *routeReaderWriterWithNoUninstall) RouteByProtocol(protocol int) ([]*routing.Route, error) {
	return r.routeReaderWriter.RouteByProtocol(protocol)
}

func (r *routeReaderWriterWithNoUninstall) RouteByProtocolAllTables(protocol int) ([]*routing.Route, error) {
	return routing.RouteByProtocolAllTables(r.routeReaderWriter, protocol)
}

func (r *routeReaderWriterWithNoUninstall) RouteForget(route *routing.Route) {
	routing.ForgetRoute(r.routeReaderWriter, route)
}

func (r *routeReaderWriterWithNoUninstall) RouteForgetVia(nextHop net.IP) {
	routing.ForgetRoutesVia(r.routeReaderWriter, nextHop)
}
