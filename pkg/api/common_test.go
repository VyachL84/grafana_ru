package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/grafana/grafana/pkg/api/response"
	"github.com/grafana/grafana/pkg/api/routing"
	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/db/dbtest"
	"github.com/grafana/grafana/pkg/infra/fs"
	"github.com/grafana/grafana/pkg/infra/localcache"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/infra/remotecache"
	"github.com/grafana/grafana/pkg/infra/tracing"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	"github.com/grafana/grafana/pkg/services/accesscontrol/acimpl"
	accesscontrolmock "github.com/grafana/grafana/pkg/services/accesscontrol/mock"
	"github.com/grafana/grafana/pkg/services/accesscontrol/ossaccesscontrol"
	"github.com/grafana/grafana/pkg/services/annotations/annotationstest"
	"github.com/grafana/grafana/pkg/services/auth/authtest"
	"github.com/grafana/grafana/pkg/services/auth/jwt"
	"github.com/grafana/grafana/pkg/services/authn/authntest"
	"github.com/grafana/grafana/pkg/services/contexthandler"
	"github.com/grafana/grafana/pkg/services/contexthandler/authproxy"
	"github.com/grafana/grafana/pkg/services/contexthandler/ctxkey"
	contextmodel "github.com/grafana/grafana/pkg/services/contexthandler/model"
	"github.com/grafana/grafana/pkg/services/dashboards"
	dashboardsstore "github.com/grafana/grafana/pkg/services/dashboards/database"
	dashboardservice "github.com/grafana/grafana/pkg/services/dashboards/service"
	dashver "github.com/grafana/grafana/pkg/services/dashboardversion"
	"github.com/grafana/grafana/pkg/services/featuremgmt"
	"github.com/grafana/grafana/pkg/services/folder/folderimpl"
	"github.com/grafana/grafana/pkg/services/folder/foldertest"
	"github.com/grafana/grafana/pkg/services/guardian"
	"github.com/grafana/grafana/pkg/services/ldap/service"
	"github.com/grafana/grafana/pkg/services/licensing"
	"github.com/grafana/grafana/pkg/services/login"
	"github.com/grafana/grafana/pkg/services/login/loginservice"
	"github.com/grafana/grafana/pkg/services/login/logintest"
	"github.com/grafana/grafana/pkg/services/org"
	"github.com/grafana/grafana/pkg/services/org/orgtest"
	"github.com/grafana/grafana/pkg/services/preference/preftest"
	"github.com/grafana/grafana/pkg/services/quota/quotatest"
	"github.com/grafana/grafana/pkg/services/rendering"
	"github.com/grafana/grafana/pkg/services/search"
	"github.com/grafana/grafana/pkg/services/search/model"
	"github.com/grafana/grafana/pkg/services/searchusers"
	"github.com/grafana/grafana/pkg/services/searchusers/filters"
	"github.com/grafana/grafana/pkg/services/sqlstore"
	"github.com/grafana/grafana/pkg/services/supportbundles/supportbundlestest"
	"github.com/grafana/grafana/pkg/services/tag/tagimpl"
	"github.com/grafana/grafana/pkg/services/team"
	"github.com/grafana/grafana/pkg/services/team/teamimpl"
	"github.com/grafana/grafana/pkg/services/team/teamtest"
	"github.com/grafana/grafana/pkg/services/user"
	"github.com/grafana/grafana/pkg/services/user/userimpl"
	"github.com/grafana/grafana/pkg/services/user/usertest"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/grafana/grafana/pkg/web"
	"github.com/grafana/grafana/pkg/web/webtest"
)

func loggedInUserScenario(t *testing.T, desc string, url string, routePattern string, fn scenarioFunc, sqlStore db.DB) {
	loggedInUserScenarioWithRole(t, desc, "GET", url, routePattern, org.RoleEditor, fn, sqlStore)
}

func loggedInUserScenarioWithRole(t *testing.T, desc string, method string, url string, routePattern string, role org.RoleType, fn scenarioFunc, sqlStore db.DB) {
	t.Run(fmt.Sprintf("%s %s", desc, url), func(t *testing.T) {
		sc := setupScenarioContext(t, url)
		sc.sqlStore = sqlStore
		sc.userService = usertest.NewUserServiceFake()
		sc.defaultHandler = routing.Wrap(func(c *contextmodel.ReqContext) response.Response {
			sc.context = c
			sc.context.UserID = testUserID
			sc.context.OrgID = testOrgID
			sc.context.Login = testUserLogin
			sc.context.OrgRole = role
			if sc.handlerFunc != nil {
				return sc.handlerFunc(sc.context)
			}

			return nil
		})

		switch method {
		case "GET":
			sc.m.Get(routePattern, sc.defaultHandler)
		case "DELETE":
			sc.m.Delete(routePattern, sc.defaultHandler)
		}
		fn(sc)
	})
}

func anonymousUserScenario(t *testing.T, desc string, method string, url string, routePattern string, fn scenarioFunc) {
	t.Run(fmt.Sprintf("%s %s", desc, url), func(t *testing.T) {
		sc := setupScenarioContext(t, url)
		sc.defaultHandler = routing.Wrap(func(c *contextmodel.ReqContext) response.Response {
			sc.context = c
			if sc.handlerFunc != nil {
				return sc.handlerFunc(sc.context)
			}

			return nil
		})

		switch method {
		case "GET":
			sc.m.Get(routePattern, sc.defaultHandler)
		case "DELETE":
			sc.m.Delete(routePattern, sc.defaultHandler)
		}

		fn(sc)
	})
}

func (sc *scenarioContext) fakeReq(method, url string) *scenarioContext {
	sc.resp = httptest.NewRecorder()
	req, err := http.NewRequest(method, url, nil)
	require.NoError(sc.t, err)
	req.Header.Add("Content-Type", "application/json")
	sc.req = req

	return sc
}

func (sc *scenarioContext) fakeReqWithParams(method, url string, queryParams map[string]string) *scenarioContext {
	sc.resp = httptest.NewRecorder()
	req, err := http.NewRequest(method, url, nil)
	// TODO: Depend on sc.t
	if sc.t != nil {
		require.NoError(sc.t, err)
	} else if err != nil {
		panic(fmt.Sprintf("Making request failed: %s", err))
	}

	req.Header.Add("Content-Type", "application/json")

	q := req.URL.Query()
	for k, v := range queryParams {
		q.Add(k, v)
	}
	req.URL.RawQuery = q.Encode()
	sc.req = req
	return sc
}

func (sc *scenarioContext) fakeReqNoAssertions(method, url string) *scenarioContext {
	sc.resp = httptest.NewRecorder()
	req, _ := http.NewRequest(method, url, nil)
	req.Header.Add("Content-Type", "application/json")
	sc.req = req

	return sc
}

func (sc *scenarioContext) fakeReqNoAssertionsWithCookie(method, url string, cookie http.Cookie) *scenarioContext {
	sc.resp = httptest.NewRecorder()
	http.SetCookie(sc.resp, &cookie)

	req, _ := http.NewRequest(method, url, nil)
	req.Header = http.Header{"Cookie": sc.resp.Header()["Set-Cookie"]}
	req.Header.Add("Content-Type", "application/json")
	sc.req = req

	return sc
}

type scenarioContext struct {
	t                       *testing.T
	cfg                     *setting.Cfg
	m                       *web.Mux
	context                 *contextmodel.ReqContext
	resp                    *httptest.ResponseRecorder
	handlerFunc             handlerFunc
	defaultHandler          web.Handler
	req                     *http.Request
	url                     string
	userAuthTokenService    *authtest.FakeUserAuthTokenService
	sqlStore                db.DB
	authInfoService         *logintest.AuthInfoServiceFake
	dashboardVersionService dashver.Service
	userService             user.Service
	dashboardService        dashboards.DashboardService
}

func (sc *scenarioContext) exec() {
	sc.m.ServeHTTP(sc.resp, sc.req)
}

type scenarioFunc func(c *scenarioContext)
type handlerFunc func(c *contextmodel.ReqContext) response.Response

func getContextHandler(t *testing.T, cfg *setting.Cfg) *contexthandler.ContextHandler {
	t.Helper()

	if cfg == nil {
		cfg = setting.NewCfg()
	}

	sqlStore := db.InitTestDB(t)
	remoteCacheSvc := &remotecache.RemoteCache{}
	cfg.RemoteCacheOptions = &setting.RemoteCacheOptions{
		Name: "database",
	}
	userAuthTokenSvc := authtest.NewFakeUserAuthTokenService()
	renderSvc := &fakeRenderService{}
	authJWTSvc := jwt.NewFakeJWTService()
	tracer := tracing.InitializeTracerForTest()
	authProxy := authproxy.ProvideAuthProxy(cfg, remoteCacheSvc, loginservice.LoginServiceMock{}, &usertest.FakeUserService{}, sqlStore, service.NewLDAPFakeService())
	loginService := &logintest.LoginServiceFake{}
	authenticator := &logintest.AuthenticatorFake{}
	ctxHdlr := contexthandler.ProvideService(cfg, userAuthTokenSvc, authJWTSvc, remoteCacheSvc, renderSvc, sqlStore, tracer, authProxy, loginService, nil, authenticator, usertest.NewUserServiceFake(), orgtest.NewOrgServiceFake(), nil, featuremgmt.WithFeatures(), &authntest.FakeService{})

	return ctxHdlr
}

func setupScenarioContext(t *testing.T, url string) *scenarioContext {
	cfg := setting.NewCfg()
	sc := &scenarioContext{
		url: url,
		t:   t,
		cfg: cfg,
	}
	viewsPath, err := filepath.Abs("../../public/views")
	require.NoError(t, err)
	exists, err := fs.Exists(viewsPath)
	require.NoError(t, err)
	require.Truef(t, exists, "Views should be in %q", viewsPath)

	sc.m = web.New()
	sc.m.UseMiddleware(web.Renderer(viewsPath, "[[", "]]"))
	sc.m.Use(getContextHandler(t, cfg).Middleware)

	return sc
}

type fakeRenderService struct {
	rendering.Service
}

func (s *fakeRenderService) Init() error {
	return nil
}

// accessControlScenarioContext contains the setups for accesscontrol tests
type accessControlScenarioContext struct {
	// server we registered hs routes on.
	server *web.Mux

	// initCtx is used in a middleware to set the initial context
	// of the request server side. Can be used to pretend sign in.
	initCtx *contextmodel.ReqContext

	// hs is a minimal HTTPServer for the accesscontrol tests to pass.
	hs *HTTPServer

	// acmock is an accesscontrol mock used to fake users rights.
	acmock *accesscontrolmock.Mock

	// db is a test database initialized with InitTestDB
	db *sqlstore.SQLStore

	// cfg is the setting provider
	cfg *setting.Cfg

	dashboardsStore             dashboards.Store
	teamService                 team.Service
	userService                 user.Service
	folderPermissionsService    *accesscontrolmock.MockPermissionsService
	dashboardPermissionsService *accesscontrolmock.MockPermissionsService
}

func userWithPermissions(orgID int64, permissions []accesscontrol.Permission) *user.SignedInUser {
	return &user.SignedInUser{OrgID: orgID, OrgRole: org.RoleViewer, Permissions: map[int64]map[string][]string{orgID: accesscontrol.GroupScopesByAction(permissions)}}
}

// setInitCtxSignedInUser sets a copy of the user in initCtx
func setInitCtxSignedInUser(initCtx *contextmodel.ReqContext, user user.SignedInUser) {
	initCtx.IsSignedIn = true
	initCtx.SignedInUser = &user
}

func setupSimpleHTTPServer(features *featuremgmt.FeatureManager) *HTTPServer {
	if features == nil {
		features = featuremgmt.WithFeatures()
	}
	cfg := setting.NewCfg()
	cfg.RBACEnabled = false
	cfg.IsFeatureToggleEnabled = features.IsEnabled

	return &HTTPServer{
		Cfg:             cfg,
		Features:        features,
		License:         &licensing.OSSLicensingService{},
		AccessControl:   acimpl.ProvideAccessControl(cfg),
		annotationsRepo: annotationstest.NewFakeAnnotationsRepo(),
		authInfoService: &logintest.AuthInfoServiceFake{
			ExpectedLabels: map[int64]string{int64(1): login.GetAuthProviderLabel(login.LDAPAuthModule)},
		},
	}
}

func setupHTTPServer(t *testing.T, useFakeAccessControl bool, options ...APITestServerOption) accessControlScenarioContext {
	return setupHTTPServerWithCfg(t, useFakeAccessControl, setting.NewCfg(), options...)
}

func setupHTTPServerWithCfg(t *testing.T, useFakeAccessControl bool, cfg *setting.Cfg, options ...APITestServerOption) accessControlScenarioContext {
	db := db.InitTestDB(t, db.InitTestDBOpt{})
	return setupHTTPServerWithCfgDb(t, useFakeAccessControl, cfg, db, db, featuremgmt.WithFeatures(), options...)
}

func setupHTTPServerWithCfgDb(
	t *testing.T, useFakeAccessControl bool, cfg *setting.Cfg, db *sqlstore.SQLStore,
	store db.DB, features *featuremgmt.FeatureManager, options ...APITestServerOption,
) accessControlScenarioContext {
	t.Helper()
	license := &licensing.OSSLicensingService{}
	routeRegister := routing.NewRouteRegister()
	teamService := teamimpl.ProvideService(db, cfg)
	cfg.IsFeatureToggleEnabled = features.IsEnabled
	quotaService := quotatest.New(false, nil)
	dashboardsStore, err := dashboardsstore.ProvideDashboardStore(db, cfg, featuremgmt.WithFeatures(), tagimpl.ProvideService(db, cfg), quotaService)
	require.NoError(t, err)

	var acmock *accesscontrolmock.Mock
	var ac accesscontrol.AccessControl
	var acService accesscontrol.Service

	var userSvc user.Service
	userMock := usertest.NewUserServiceFake()
	userMock.ExpectedUser = &user.User{ID: 1}
	orgMock := orgtest.NewOrgServiceFake()
	orgMock.ExpectedOrg = &org.Org{}
	orgMock.ExpectedSearchOrgUsersResult = &org.SearchOrgUsersQueryResult{}

	// Defining the accesscontrol service has to be done before registering routes
	if useFakeAccessControl {
		acmock = accesscontrolmock.New()
		if !cfg.RBACEnabled {
			acmock = acmock.WithDisabled()
		}
		ac = acmock
		acService = acmock
		userSvc = userMock
	} else {
		var err error
		ac = acimpl.ProvideAccessControl(cfg)
		userSvc, err = userimpl.ProvideService(db, nil, cfg, teamimpl.ProvideService(db, cfg), localcache.ProvideService(), quotatest.New(false, nil), supportbundlestest.NewFakeBundleService())
		require.NoError(t, err)
		acService, err = acimpl.ProvideService(cfg, db, routeRegister, localcache.ProvideService(), ac, featuremgmt.WithFeatures())
		require.NoError(t, err)
	}
	teamPermissionService, err := ossaccesscontrol.ProvideTeamPermissions(cfg, routeRegister, db, ac, license, acService, teamService, userSvc)
	require.NoError(t, err)

	folderPermissionsService := accesscontrolmock.NewMockedPermissionsService()
	dashboardPermissionsService := accesscontrolmock.NewMockedPermissionsService()

	folderSvc := foldertest.NewFakeService()

	// Create minimal HTTP Server
	hs := &HTTPServer{
		Cfg:                    cfg,
		Features:               features,
		Live:                   newTestLive(t, db),
		QuotaService:           quotaService,
		RouteRegister:          routeRegister,
		SQLStore:               store,
		License:                &licensing.OSSLicensingService{},
		AccessControl:          ac,
		accesscontrolService:   acService,
		teamPermissionsService: teamPermissionService,
		searchUsersService:     searchusers.ProvideUsersService(filters.ProvideOSSSearchUserFilter(), usertest.NewUserServiceFake()),
		DashboardService: dashboardservice.ProvideDashboardService(
			cfg, dashboardsStore, folderimpl.ProvideDashboardFolderStore(db), nil, features,
			folderPermissionsService, dashboardPermissionsService, ac,
			folderSvc,
		),
		preferenceService: preftest.NewPreferenceServiceFake(),
		userService:       userSvc,
		orgService:        orgMock,
		teamService:       teamService,
		annotationsRepo:   annotationstest.NewFakeAnnotationsRepo(),
		authInfoService: &logintest.AuthInfoServiceFake{
			ExpectedLabels: map[int64]string{int64(1): login.GetAuthProviderLabel(login.LDAPAuthModule)},
		},
	}

	for _, o := range options {
		o(hs)
	}

	require.NoError(t, hs.declareFixedRoles())
	require.NoError(t, hs.accesscontrolService.(accesscontrol.RoleRegistry).RegisterFixedRoles(context.Background()))

	// Instantiate a new Server
	m := web.New()

	// middleware to set the test initial context
	initCtx := &contextmodel.ReqContext{}
	m.Use(func(c *web.Context) {
		initCtx.Context = c
		initCtx.Logger = log.New("api-test")
		c.Req = c.Req.WithContext(ctxkey.Set(c.Req.Context(), initCtx))
	})

	m.Use(accesscontrol.LoadPermissionsMiddleware(hs.accesscontrolService))

	// Register all routes
	hs.registerRoutes()
	hs.RouteRegister.Register(m.Router)

	return accessControlScenarioContext{
		server:                      m,
		initCtx:                     initCtx,
		hs:                          hs,
		acmock:                      acmock,
		db:                          db,
		cfg:                         cfg,
		dashboardsStore:             dashboardsStore,
		teamService:                 teamService,
		userService:                 userSvc,
		dashboardPermissionsService: dashboardPermissionsService,
		folderPermissionsService:    folderPermissionsService,
	}
}

func callAPI(server *web.Mux, method, path string, body io.Reader, t *testing.T) *httptest.ResponseRecorder {
	req, err := http.NewRequest(method, path, body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder
}

func mockRequestBody(v interface{}) io.ReadCloser {
	b, _ := json.Marshal(v)
	return io.NopCloser(bytes.NewReader(b))
}

// APITestServerOption option func for customizing HTTPServer configuration
// when setting up an API test server via SetupAPITestServer.
type APITestServerOption func(hs *HTTPServer)

// SetupAPITestServer sets up a webtest.Server ready for testing all
// routes registered via HTTPServer.registerRoutes().
// Optionally customize HTTPServer configuration by providing APITestServerOption
// option(s).
func SetupAPITestServer(t *testing.T, opts ...APITestServerOption) *webtest.Server {
	t.Helper()

	hs := &HTTPServer{
		RouteRegister:      routing.NewRouteRegister(),
		License:            &licensing.OSSLicensingService{},
		Features:           featuremgmt.WithFeatures(),
		QuotaService:       quotatest.New(false, nil),
		searchUsersService: &searchusers.OSSService{},
	}

	for _, opt := range opts {
		opt(hs)
	}

	if hs.Cfg == nil {
		hs.Cfg = setting.NewCfg()
		hs.Cfg.RBACEnabled = false
	}

	if hs.AccessControl == nil {
		hs.AccessControl = acimpl.ProvideAccessControl(hs.Cfg)
	}

	hs.registerRoutes()

	s := webtest.NewServer(t, hs.RouteRegister)
	return s
}

var (
	viewerRole = org.RoleViewer
	editorRole = org.RoleEditor
)

type setUpConf struct {
	aclMockResp []*dashboards.DashboardACLInfoDTO
}

type mockSearchService struct{ ExpectedResult model.HitList }

func (mss *mockSearchService) SearchHandler(_ context.Context, q *search.Query) error {
	q.Result = mss.ExpectedResult
	return nil
}
func (mss *mockSearchService) SortOptions() []model.SortOption { return nil }

func setUp(confs ...setUpConf) *HTTPServer {
	store := dbtest.NewFakeDB()
	hs := &HTTPServer{SQLStore: store, SearchService: &mockSearchService{}}

	aclMockResp := []*dashboards.DashboardACLInfoDTO{}
	for _, c := range confs {
		if c.aclMockResp != nil {
			aclMockResp = c.aclMockResp
		}
	}
	teamSvc := &teamtest.FakeService{}
	dashSvc := &dashboards.FakeDashboardService{}
	qResult := aclMockResp
	dashSvc.On("GetDashboardACLInfoList", mock.Anything, mock.AnythingOfType("*dashboards.GetDashboardACLInfoListQuery")).Return(qResult, nil)
	guardian.InitLegacyGuardian(store, dashSvc, teamSvc)
	return hs
}
