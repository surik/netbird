package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"

	"github.com/netbirdio/netbird/management/server/http/api"
	"github.com/netbirdio/netbird/management/server/status"
	"github.com/netbirdio/netbird/route"

	"github.com/gorilla/mux"
	"github.com/magiconair/properties/assert"

	"github.com/netbirdio/netbird/management/server"
	"github.com/netbirdio/netbird/management/server/jwtclaims"
	"github.com/netbirdio/netbird/management/server/mock_server"
)

const (
	existingRouteID = "existingRouteID"
	notFoundRouteID = "notFoundRouteID"
	existingPeerIP  = "100.64.0.100"
	notFoundPeerID  = "nonExistingPeer"
	existingPeerKey = "existingPeerKey"
	testAccountID   = "test_id"
	existingGroupID = "testGroup"
	notFoundGroupID = "nonExistingGroup"
)

var existingPeerID = "peer-id"

var baseExistingRoute = &route.Route{
	ID:          existingRouteID,
	Description: "base route",
	NetID:       "awesomeNet",
	Network:     netip.MustParsePrefix("192.168.0.0/24"),
	NetworkType: route.IPv4Network,
	Metric:      9999,
	Masquerade:  false,
	Enabled:     true,
	Groups:      []string{existingGroupID},
}

var testingAccount = &server.Account{
	Id:     testAccountID,
	Domain: "hotmail.com",
	Peers: map[string]*server.Peer{
		existingPeerID: {
			Key: existingPeerKey,
			IP:  netip.MustParseAddr(existingPeerIP).AsSlice(),
			ID:  existingPeerID,
		},
	},
	Users: map[string]*server.User{
		"test_user": server.NewAdminUser("test_user"),
	},
}

func initRoutesTestData() *RoutesHandler {
	return &RoutesHandler{
		accountManager: &mock_server.MockAccountManager{
			GetRouteFunc: func(_, routeID, _ string) (*route.Route, error) {
				if routeID == existingRouteID {
					return baseExistingRoute, nil
				}
				return nil, status.Errorf(status.NotFound, "route with ID %s not found", routeID)
			},
			CreateRouteFunc: func(accountID string, network, peerID, peersGroup, description, netID string, masquerade bool, metric int, groups []string, enabled bool, _ string) (*route.Route, error) {
				if peerID == notFoundPeerID {
					return nil, status.Errorf(status.InvalidArgument, "peer with ID %s not found", peerID)
				}
				if peersGroup == notFoundGroupID {
					return nil, status.Errorf(status.InvalidArgument, "peers group with ID %s not found", peersGroup)
				}
				networkType, p, _ := route.ParseNetwork(network)
				return &route.Route{
					ID:          existingRouteID,
					NetID:       netID,
					Peer:        peerID,
					PeersGroup:  peersGroup,
					Network:     p,
					NetworkType: networkType,
					Description: description,
					Masquerade:  masquerade,
					Enabled:     enabled,
					Groups:      groups,
				}, nil
			},
			SaveRouteFunc: func(_, _ string, r *route.Route) error {
				if r.Peer == notFoundPeerID {
					return status.Errorf(status.InvalidArgument, "peer with ID %s not found", r.Peer)
				}
				return nil
			},
			DeleteRouteFunc: func(_ string, routeID string, _ string) error {
				if routeID != existingRouteID {
					return status.Errorf(status.NotFound, "Peer with ID %s not found", routeID)
				}
				return nil
			},
			GetPeerByIPFunc: func(_ string, peerIP string) (*server.Peer, error) {
				if peerIP != existingPeerID {
					return nil, status.Errorf(status.NotFound, "Peer with ID %s not found", peerIP)
				}
				return &server.Peer{
					Key: existingPeerKey,
					IP:  netip.MustParseAddr(existingPeerID).AsSlice(),
				}, nil
			},
			UpdateRouteFunc: func(_ string, routeID string, operations []server.RouteUpdateOperation) (*route.Route, error) {
				routeToUpdate := baseExistingRoute
				if routeID != routeToUpdate.ID {
					return nil, status.Errorf(status.NotFound, "route %s no longer exists", routeID)
				}
				for _, operation := range operations {
					switch operation.Type {
					case server.UpdateRouteNetwork:
						routeToUpdate.NetworkType, routeToUpdate.Network, _ = route.ParseNetwork(operation.Values[0])
					case server.UpdateRouteDescription:
						routeToUpdate.Description = operation.Values[0]
					case server.UpdateRouteNetworkIdentifier:
						routeToUpdate.NetID = operation.Values[0]
					case server.UpdateRoutePeer:
						routeToUpdate.Peer = operation.Values[0]
						if routeToUpdate.Peer == notFoundPeerID {
							return nil, status.Errorf(status.InvalidArgument, "peer with ID %s not found", routeToUpdate.Peer)
						}
					case server.UpdateRouteMetric:
						routeToUpdate.Metric, _ = strconv.Atoi(operation.Values[0])
					case server.UpdateRouteMasquerade:
						routeToUpdate.Masquerade, _ = strconv.ParseBool(operation.Values[0])
					case server.UpdateRouteEnabled:
						routeToUpdate.Enabled, _ = strconv.ParseBool(operation.Values[0])
					case server.UpdateRouteGroups:
						routeToUpdate.Groups = operation.Values
					default:
						return nil, fmt.Errorf("no operation")
					}
				}
				return routeToUpdate, nil
			},
			GetAccountFromTokenFunc: func(_ jwtclaims.AuthorizationClaims) (*server.Account, *server.User, error) {
				return testingAccount, testingAccount.Users["test_user"], nil
			},
		},
		claimsExtractor: jwtclaims.NewClaimsExtractor(
			jwtclaims.WithFromRequestContext(func(r *http.Request) jwtclaims.AuthorizationClaims {
				return jwtclaims.AuthorizationClaims{
					UserId:    "test_user",
					Domain:    "hotmail.com",
					AccountId: testAccountID,
				}
			}),
		),
	}
}

func TestRoutesHandlers(t *testing.T) {
	tt := []struct {
		name           string
		expectedStatus int
		expectedBody   bool
		expectedRoute  *api.Route
		requestType    string
		requestPath    string
		requestBody    io.Reader
	}{
		{
			name:           "Get Existing Route",
			requestType:    http.MethodGet,
			requestPath:    "/api/routes/" + existingRouteID,
			expectedStatus: http.StatusOK,
			expectedBody:   true,
			expectedRoute:  toRouteResponse(baseExistingRoute),
		},
		{
			name:           "Get Not Existing Route",
			requestType:    http.MethodGet,
			requestPath:    "/api/routes/" + notFoundRouteID,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "Delete Existing Route",
			requestType:    http.MethodDelete,
			requestPath:    "/api/routes/" + existingRouteID,
			expectedStatus: http.StatusOK,
			expectedBody:   false,
		},
		{
			name:           "Delete Not Existing Route",
			requestType:    http.MethodDelete,
			requestPath:    "/api/routes/" + notFoundRouteID,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:        "POST OK",
			requestType: http.MethodPost,
			requestPath: "/api/routes",
			requestBody: bytes.NewBuffer(
				[]byte(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID))),
			expectedStatus: http.StatusOK,
			expectedBody:   true,
			expectedRoute: &api.Route{
				Id:          existingRouteID,
				Description: "Post",
				NetworkId:   "awesomeNet",
				Network:     "192.168.0.0/16",
				Peer:        &existingPeerID,
				NetworkType: route.IPv4NetworkString,
				Masquerade:  false,
				Enabled:     false,
				Groups:      []string{existingGroupID},
			},
		},
		{
			name:           "POST Not Found Peer",
			requestType:    http.MethodPost,
			requestPath:    "/api/routes",
			requestBody:    bytes.NewBufferString(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", notFoundPeerID, existingGroupID)),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:           "POST Invalid Network Identifier",
			requestType:    http.MethodPost,
			requestPath:    "/api/routes",
			requestBody:    bytes.NewBufferString(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"12345678901234567890qwertyuiopqwertyuiop1\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID)),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:           "POST Invalid Network",
			requestType:    http.MethodPost,
			requestPath:    "/api/routes",
			requestBody:    bytes.NewBufferString(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/34\",\"network_id\":\"awesomeNet\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID)),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:        "POST UnprocessableEntity when both peer and peers_group are provided",
			requestType: http.MethodPost,
			requestPath: "/api/routes",
			requestBody: bytes.NewBuffer(
				[]byte(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"peer\":\"%s\",\"peers_group\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID, existingGroupID))),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:        "POST UnprocessableEntity when no peer and peers_group are provided",
			requestType: http.MethodPost,
			requestPath: "/api/routes",
			requestBody: bytes.NewBuffer(
				[]byte(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"groups\":[\"%s\"]}", existingPeerID))),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:           "PUT OK",
			requestType:    http.MethodPut,
			requestPath:    "/api/routes/" + existingRouteID,
			requestBody:    bytes.NewBufferString(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID)),
			expectedStatus: http.StatusOK,
			expectedBody:   true,
			expectedRoute: &api.Route{
				Id:          existingRouteID,
				Description: "Post",
				NetworkId:   "awesomeNet",
				Network:     "192.168.0.0/16",
				Peer:        &existingPeerID,
				NetworkType: route.IPv4NetworkString,
				Masquerade:  false,
				Enabled:     false,
				Groups:      []string{existingGroupID},
			},
		},
		{
			name:           "PUT Not Found Route",
			requestType:    http.MethodPut,
			requestPath:    "/api/routes/" + notFoundRouteID,
			requestBody:    bytes.NewBufferString(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID)),
			expectedStatus: http.StatusNotFound,
			expectedBody:   false,
		},
		{
			name:           "PUT Not Found Peer",
			requestType:    http.MethodPut,
			requestPath:    "/api/routes/" + existingRouteID,
			requestBody:    bytes.NewBufferString(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", notFoundPeerID, existingGroupID)),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:           "PUT Invalid Network Identifier",
			requestType:    http.MethodPut,
			requestPath:    "/api/routes/" + existingRouteID,
			requestBody:    bytes.NewBufferString(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"12345678901234567890qwertyuiopqwertyuiop1\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID)),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:           "PUT Invalid Network",
			requestType:    http.MethodPut,
			requestPath:    "/api/routes/" + existingRouteID,
			requestBody:    bytes.NewBufferString(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/34\",\"network_id\":\"awesomeNet\",\"Peer\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID)),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:        "PUT UnprocessableEntity when both peer and peers_group are provided",
			requestType: http.MethodPut,
			requestPath: "/api/routes/" + existingRouteID,
			requestBody: bytes.NewBuffer(
				[]byte(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"peer\":\"%s\",\"peers_group\":\"%s\",\"groups\":[\"%s\"]}", existingPeerID, existingGroupID, existingGroupID))),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
		{
			name:        "PUT UnprocessableEntity when no peer and peers_group are provided",
			requestType: http.MethodPut,
			requestPath: "/api/routes/" + existingRouteID,
			requestBody: bytes.NewBuffer(
				[]byte(fmt.Sprintf("{\"Description\":\"Post\",\"Network\":\"192.168.0.0/16\",\"network_id\":\"awesomeNet\",\"groups\":[\"%s\"]}", existingPeerID))),
			expectedStatus: http.StatusUnprocessableEntity,
			expectedBody:   false,
		},
	}

	p := initRoutesTestData()

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(tc.requestType, tc.requestPath, tc.requestBody)

			router := mux.NewRouter()
			router.HandleFunc("/api/routes/{routeId}", p.GetRoute).Methods("GET")
			router.HandleFunc("/api/routes/{routeId}", p.DeleteRoute).Methods("DELETE")
			router.HandleFunc("/api/routes", p.CreateRoute).Methods("POST")
			router.HandleFunc("/api/routes/{routeId}", p.UpdateRoute).Methods("PUT")
			router.ServeHTTP(recorder, req)

			res := recorder.Result()
			defer res.Body.Close()

			content, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("I don't know what I expected; %v", err)
			}

			if status := recorder.Code; status != tc.expectedStatus {
				t.Errorf("handler returned wrong status code: got %v want %v, content: %s",
					status, tc.expectedStatus, string(content))
				return
			}

			if !tc.expectedBody {
				return
			}

			got := &api.Route{}
			if err = json.Unmarshal(content, &got); err != nil {
				t.Fatalf("Sent content is not in correct json format; %v", err)
			}
			assert.Equal(t, got, tc.expectedRoute)
		})
	}
}
