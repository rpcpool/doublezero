package routing

import "fmt"

// AllTablesRouteLister lists routes by protocol in every routing table, not
// just the main table that Netlinker.RouteByProtocol covers.
type AllTablesRouteLister interface {
	RouteByProtocolAllTables(protocol int) ([]*Route, error)
}

// RouteByProtocolAllTables calls w.RouteByProtocolAllTables, or returns an
// error if w cannot list routes outside the main table.
func RouteByProtocolAllTables(w any, protocol int) ([]*Route, error) {
	l, ok := w.(AllTablesRouteLister)
	if !ok {
		return nil, fmt.Errorf("route lister %T cannot list routes outside the main table", w)
	}
	return l.RouteByProtocolAllTables(protocol)
}
