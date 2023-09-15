package server

import (
	"net/netip"
	"strconv"
	"unicode/utf8"

	"github.com/netbirdio/netbird/management/proto"
	"github.com/netbirdio/netbird/management/server/activity"
	"github.com/netbirdio/netbird/management/server/status"
	"github.com/netbirdio/netbird/route"
	"github.com/rs/xid"
	log "github.com/sirupsen/logrus"
)

const (
	// UpdateRouteDescription indicates a route description update operation
	UpdateRouteDescription RouteUpdateOperationType = iota
	// UpdateRouteNetwork indicates a route IP update operation
	UpdateRouteNetwork
	// UpdateRoutePeer indicates a route peer update operation
	UpdateRoutePeer
	// UpdateRouteMetric indicates a route metric update operation
	UpdateRouteMetric
	// UpdateRouteMasquerade indicates a route masquerade update operation
	UpdateRouteMasquerade
	// UpdateRouteEnabled indicates a route enabled update operation
	UpdateRouteEnabled
	// UpdateRouteNetworkIdentifier indicates a route net ID update operation
	UpdateRouteNetworkIdentifier
	// UpdateRouteGroups indicates a group list update operation
	UpdateRouteGroups
)

// RouteUpdateOperationType operation type
type RouteUpdateOperationType int

func (t RouteUpdateOperationType) String() string {
	switch t {
	case UpdateRouteDescription:
		return "UpdateRouteDescription"
	case UpdateRouteNetwork:
		return "UpdateRouteNetwork"
	case UpdateRoutePeer:
		return "UpdateRoutePeer"
	case UpdateRouteMetric:
		return "UpdateRouteMetric"
	case UpdateRouteMasquerade:
		return "UpdateRouteMasquerade"
	case UpdateRouteEnabled:
		return "UpdateRouteEnabled"
	case UpdateRouteNetworkIdentifier:
		return "UpdateRouteNetworkIdentifier"
	case UpdateRouteGroups:
		return "UpdateRouteGroups"
	default:
		return "InvalidOperation"
	}
}

// RouteUpdateOperation operation object with type and values to be applied
type RouteUpdateOperation struct {
	Type   RouteUpdateOperationType
	Values []string
}

// GetRoute gets a route object from account and route IDs
func (am *DefaultAccountManager) GetRoute(accountID, routeID, userID string) (*route.Route, error) {
	unlock := am.Store.AcquireAccountLock(accountID)
	defer unlock()

	account, err := am.Store.GetAccount(accountID)
	if err != nil {
		return nil, err
	}

	user, err := account.FindUser(userID)
	if err != nil {
		return nil, err
	}

	if !user.IsAdmin() {
		return nil, status.Errorf(status.PermissionDenied, "Only administrators can view Network Routes")
	}

	wantedRoute, found := account.Routes[routeID]
	if found {
		return wantedRoute, nil
	}

	return nil, status.Errorf(status.NotFound, "route with ID %s not found", routeID)
}

// checkPrefixPeerExists checks the combination of prefix and peer id, if it exists returns an error, otherwise returns nil
func (am *DefaultAccountManager) checkPrefixPeerExists(accountID, peerID string, prefix netip.Prefix) error {
	if peerID == "" {
		return nil
	}

	account, err := am.Store.GetAccount(accountID)
	if err != nil {
		return err
	}

	routesWithPrefix := account.GetRoutesByPrefix(prefix)

	for _, prefixRoute := range routesWithPrefix {
		if prefixRoute.Peer == peerID {
			return status.Errorf(status.AlreadyExists, "failed to add route with prefix %s - peer already has this route", prefix.String())
		}
	}
	return nil
}

// checkPrefixPeersGroupExists checks the combination of prefix and peer ids from the group, if it exists returns an error, otherwise returns nil
func (am *DefaultAccountManager) checkPrefixPeersGroupExists(accountID, peersGroupID string, prefix netip.Prefix) error {
	if peersGroupID == "" {
		return nil
	}

	account, err := am.Store.GetAccount(accountID)
	if err != nil {
		return err
	}

	routesWithPrefix := account.GetRoutesByPrefix(prefix)

	group := account.GetGroup(peersGroupID)
	if group == nil {
		return nil
	}

	for _, prefixRoute := range routesWithPrefix {
		for _, peerID := range group.Peers {
			if prefixRoute.Peer == peerID {
				return status.Errorf(
					status.AlreadyExists, "failed to add route with prefix %s - peer %s already has this route",
					prefix.String(), peerID)
			}
		}
	}
	return nil
}

// CreateRoute creates and saves a new route
func (am *DefaultAccountManager) CreateRoute(accountID string, network, peerID, peersGroupId, description, netID string, masquerade bool, metric int, groups []string, enabled bool, userID string) (*route.Route, error) {
	unlock := am.Store.AcquireAccountLock(accountID)
	defer unlock()

	account, err := am.Store.GetAccount(accountID)
	if err != nil {
		return nil, err
	}

	if peerID != "" && peersGroupId != "" {
		return nil, status.Errorf(
			status.InvalidArgument,
			"peer with ID %s and peers group %s should not be provided at the same time",
			peerID, peersGroupId)
	}

	if peerID != "" {
		peer := account.GetPeer(peerID)
		if peer == nil {
			return nil, status.Errorf(status.InvalidArgument, "peer with ID %s not found", peerID)
		}
	}

	if peersGroupId != "" {
		group := account.GetGroup(peersGroupId)
		if group == nil {
			return nil, status.Errorf(status.InvalidArgument, "peers group with ID %s not found", peersGroupId)
		}
	}

	var newRoute route.Route
	prefixType, newPrefix, err := route.ParseNetwork(network)
	if err != nil {
		return nil, status.Errorf(status.InvalidArgument, "failed to parse IP %s", network)
	}

	err = am.checkPrefixPeerExists(accountID, peerID, newPrefix)
	if err != nil {
		return nil, err
	}

	err = am.checkPrefixPeersGroupExists(accountID, peersGroupId, newPrefix)
	if err != nil {
		return nil, err
	}

	if metric < route.MinMetric || metric > route.MaxMetric {
		return nil, status.Errorf(status.InvalidArgument, "metric should be between %d and %d", route.MinMetric, route.MaxMetric)
	}

	if utf8.RuneCountInString(netID) > route.MaxNetIDChar || netID == "" {
		return nil, status.Errorf(status.InvalidArgument, "identifier should be between 1 and %d", route.MaxNetIDChar)
	}

	err = validateGroups(groups, account.Groups)
	if err != nil {
		return nil, err
	}

	newRoute.Peer = peerID
	newRoute.PeersGroup = peersGroupId
	newRoute.ID = xid.New().String()
	newRoute.Network = newPrefix
	newRoute.NetworkType = prefixType
	newRoute.Description = description
	newRoute.NetID = netID
	newRoute.Masquerade = masquerade
	newRoute.Metric = metric
	newRoute.Enabled = enabled
	newRoute.Groups = groups

	if account.Routes == nil {
		account.Routes = make(map[string]*route.Route)
	}

	account.Routes[newRoute.ID] = &newRoute

	account.Network.IncSerial()
	if err = am.Store.SaveAccount(account); err != nil {
		return nil, err
	}

	err = am.updateAccountPeers(account)
	if err != nil {
		log.Error(err)
		return &newRoute, status.Errorf(status.Internal, "failed to update peers after create route %s", newPrefix)
	}

	am.storeEvent(userID, newRoute.ID, accountID, activity.RouteCreated, newRoute.EventMeta())

	return &newRoute, nil
}

// SaveRoute saves route
func (am *DefaultAccountManager) SaveRoute(accountID, userID string, routeToSave *route.Route) error {
	unlock := am.Store.AcquireAccountLock(accountID)
	defer unlock()

	if routeToSave == nil {
		return status.Errorf(status.InvalidArgument, "route provided is nil")
	}

	if !routeToSave.Network.IsValid() {
		return status.Errorf(status.InvalidArgument, "invalid Prefix %s", routeToSave.Network.String())
	}

	if routeToSave.Metric < route.MinMetric || routeToSave.Metric > route.MaxMetric {
		return status.Errorf(status.InvalidArgument, "metric should be between %d and %d", route.MinMetric, route.MaxMetric)
	}

	if utf8.RuneCountInString(routeToSave.NetID) > route.MaxNetIDChar || routeToSave.NetID == "" {
		return status.Errorf(status.InvalidArgument, "identifier should be between 1 and %d", route.MaxNetIDChar)
	}

	account, err := am.Store.GetAccount(accountID)
	if err != nil {
		return err
	}

	if routeToSave.Peer != "" && routeToSave.PeersGroup != "" {
		return status.Errorf(
			status.InvalidArgument,
			"peer with ID %s and peers group %s should not be provided at the same time",
			routeToSave.Peer, routeToSave.PeersGroup)
	}

	if routeToSave.Peer != "" {
		peer := account.GetPeer(routeToSave.Peer)
		if peer == nil {
			return status.Errorf(status.InvalidArgument, "peer with ID %s not found", routeToSave.Peer)
		}
	}

	if routeToSave.PeersGroup != "" {
		peer := account.GetGroup(routeToSave.PeersGroup)
		if peer == nil {
			return status.Errorf(status.InvalidArgument, "peers group with ID %s not found", routeToSave.PeersGroup)
		}
	}

	err = validateGroups(routeToSave.Groups, account.Groups)
	if err != nil {
		return err
	}

	account.Routes[routeToSave.ID] = routeToSave

	account.Network.IncSerial()
	if err = am.Store.SaveAccount(account); err != nil {
		return err
	}

	err = am.updateAccountPeers(account)
	if err != nil {
		return err
	}

	am.storeEvent(userID, routeToSave.ID, accountID, activity.RouteUpdated, routeToSave.EventMeta())

	return nil
}

// UpdateRoute updates existing route with set of operations
func (am *DefaultAccountManager) UpdateRoute(accountID, routeID string, operations []RouteUpdateOperation) (*route.Route, error) {
	unlock := am.Store.AcquireAccountLock(accountID)
	defer unlock()

	account, err := am.Store.GetAccount(accountID)
	if err != nil {
		return nil, err
	}

	routeToUpdate, ok := account.Routes[routeID]
	if !ok {
		return nil, status.Errorf(status.NotFound, "route %s no longer exists", routeID)
	}

	newRoute := routeToUpdate.Copy()

	for _, operation := range operations {

		if len(operation.Values) != 1 {
			return nil, status.Errorf(status.InvalidArgument, "operation %s contains invalid number of values, it should be 1", operation.Type.String())
		}

		switch operation.Type {
		case UpdateRouteDescription:
			newRoute.Description = operation.Values[0]
		case UpdateRouteNetworkIdentifier:
			if utf8.RuneCountInString(operation.Values[0]) > route.MaxNetIDChar || operation.Values[0] == "" {
				return nil, status.Errorf(status.InvalidArgument, "identifier should be between 1 and %d", route.MaxNetIDChar)
			}
			newRoute.NetID = operation.Values[0]
		case UpdateRouteNetwork:
			prefixType, prefix, err := route.ParseNetwork(operation.Values[0])
			if err != nil {
				return nil, status.Errorf(status.InvalidArgument, "failed to parse IP %s", operation.Values[0])
			}
			err = am.checkPrefixPeerExists(accountID, routeToUpdate.Peer, prefix)
			if err != nil {
				return nil, err
			}
			newRoute.Network = prefix
			newRoute.NetworkType = prefixType
		case UpdateRoutePeer:
			if operation.Values[0] != "" {
				peer := account.GetPeer(operation.Values[0])
				if peer == nil {
					return nil, status.Errorf(status.InvalidArgument, "peer with ID %s not found", operation.Values[0])
				}
			}

			err = am.checkPrefixPeerExists(accountID, operation.Values[0], routeToUpdate.Network)
			if err != nil {
				return nil, err
			}
			newRoute.Peer = operation.Values[0]
		case UpdateRouteMetric:
			metric, err := strconv.Atoi(operation.Values[0])
			if err != nil {
				return nil, status.Errorf(status.InvalidArgument, "failed to parse metric %s, not int", operation.Values[0])
			}
			if metric < route.MinMetric || metric > route.MaxMetric {
				return nil, status.Errorf(status.InvalidArgument, "failed to parse metric %s, value should be %d > N < %d",
					operation.Values[0],
					route.MinMetric,
					route.MaxMetric,
				)
			}
			newRoute.Metric = metric
		case UpdateRouteMasquerade:
			masquerade, err := strconv.ParseBool(operation.Values[0])
			if err != nil {
				return nil, status.Errorf(status.InvalidArgument, "failed to parse masquerade %s, not boolean", operation.Values[0])
			}
			newRoute.Masquerade = masquerade
		case UpdateRouteEnabled:
			enabled, err := strconv.ParseBool(operation.Values[0])
			if err != nil {
				return nil, status.Errorf(status.InvalidArgument, "failed to parse enabled %s, not boolean", operation.Values[0])
			}
			newRoute.Enabled = enabled
		case UpdateRouteGroups:
			err = validateGroups(operation.Values, account.Groups)
			if err != nil {
				return nil, err
			}
			newRoute.Groups = operation.Values
		}
	}

	account.Routes[routeID] = newRoute

	account.Network.IncSerial()
	if err = am.Store.SaveAccount(account); err != nil {
		return nil, err
	}

	err = am.updateAccountPeers(account)
	if err != nil {
		return nil, status.Errorf(status.Internal, "failed to update account peers")
	}
	return newRoute, nil
}

// DeleteRoute deletes route with routeID
func (am *DefaultAccountManager) DeleteRoute(accountID, routeID, userID string) error {
	unlock := am.Store.AcquireAccountLock(accountID)
	defer unlock()

	account, err := am.Store.GetAccount(accountID)
	if err != nil {
		return err
	}

	routy := account.Routes[routeID]
	if routy == nil {
		return status.Errorf(status.NotFound, "route with ID %s doesn't exist", routeID)
	}
	delete(account.Routes, routeID)

	account.Network.IncSerial()
	if err = am.Store.SaveAccount(account); err != nil {
		return err
	}

	am.storeEvent(userID, routy.ID, accountID, activity.RouteRemoved, routy.EventMeta())

	return am.updateAccountPeers(account)
}

// ListRoutes returns a list of routes from account
func (am *DefaultAccountManager) ListRoutes(accountID, userID string) ([]*route.Route, error) {
	unlock := am.Store.AcquireAccountLock(accountID)
	defer unlock()

	account, err := am.Store.GetAccount(accountID)
	if err != nil {
		return nil, err
	}

	user, err := account.FindUser(userID)
	if err != nil {
		return nil, err
	}

	if !user.IsAdmin() {
		return nil, status.Errorf(status.PermissionDenied, "Only administrators can view Network Routes")
	}

	routes := make([]*route.Route, 0, len(account.Routes))
	for _, item := range account.Routes {
		routes = append(routes, item)
	}

	return routes, nil
}

func toProtocolRoute(route *route.Route) *proto.Route {
	return &proto.Route{
		ID:          route.ID,
		NetID:       route.NetID,
		Network:     route.Network.String(),
		NetworkType: int64(route.NetworkType),
		Peer:        route.Peer,
		Metric:      int64(route.Metric),
		Masquerade:  route.Masquerade,
	}
}

func toProtocolRoutes(routes []*route.Route) []*proto.Route {
	protoRoutes := make([]*proto.Route, 0)
	for _, r := range routes {
		protoRoutes = append(protoRoutes, toProtocolRoute(r))
	}
	return protoRoutes
}
