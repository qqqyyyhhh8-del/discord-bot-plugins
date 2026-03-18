package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"discord-bot-plugins/sdk/pluginapi"
)

const (
	webSettingsCollection = "memory_web"
	webSettingsKey        = "global"

	defaultMemoryWebAddr = "127.0.0.1:17888"
	memoryWebAddrEnv     = "SELF_LEARNING_WEB_ADDR"
	memoryWebPublicEnv   = "SELF_LEARNING_WEB_PUBLIC_URL"

	maxScopeOptions    = 200
	maxPersonaPreview  = 12
	webRequestTimeout  = 25 * time.Second
	webServerCloseWait = 5 * time.Second
)

type webSettings struct {
	AccessToken string `json:"access_token,omitempty"`
}

type memoryWebRuntime struct {
	host          *pluginapi.HostClient
	listenAddr    string
	publicBaseURL string
	server        *http.Server
	startedAt     time.Time

	mu          sync.RWMutex
	accessToken string
	lastError   string
}

type memoryWebStatus struct {
	Running      bool   `json:"running"`
	ListenAddr   string `json:"listen_addr,omitempty"`
	LocalURL     string `json:"local_url,omitempty"`
	PublicURL    string `json:"public_url,omitempty"`
	AccessURL    string `json:"access_url,omitempty"`
	TokenPreview string `json:"token_preview,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	Error        string `json:"error,omitempty"`
}

type bootstrapResponse struct {
	GeneratedAt string            `json:"generated_at"`
	Web         memoryWebStatus   `json:"web"`
	Scopes      []memoryScopeItem `json:"scopes"`
	Hints       []string          `json:"hints,omitempty"`
}

type memoryScopeItem struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	Enabled        bool   `json:"enabled"`
	LastLearnedAt  string `json:"last_learned_at,omitempty"`
	EventCount     int    `json:"event_count"`
	EpisodeCount   int    `json:"episode_count"`
	CardCount      int    `json:"card_count"`
	TargetCount    int    `json:"target_count"`
	RelationCount  int    `json:"relation_count"`
	HighlightCount int    `json:"highlight_count"`
}

type memoryScopeStateResponse struct {
	Scope        memoryScopeItem      `json:"scope"`
	Web          memoryWebStatus      `json:"web"`
	Config       learningConfig       `json:"config"`
	Metrics      memoryScopeMetrics   `json:"metrics"`
	Profile      memoryProfileView    `json:"profile"`
	Persona      memoryPersonaView    `json:"persona"`
	RecentEvents []memoryEventView    `json:"recent_events"`
	Episodes     []memoryEpisodeView  `json:"episodes"`
	Cards        []memoryCardView     `json:"cards"`
	Budget       contextBudgetPreview `json:"budget"`
	Graph        memoryGraphPayload   `json:"graph"`
}

type memoryScopeMetrics struct {
	Enabled          bool           `json:"enabled"`
	EventCount       int            `json:"event_count"`
	EpisodeCount     int            `json:"episode_count"`
	CardCount        int            `json:"card_count"`
	TargetCount      int            `json:"target_count"`
	RelationCount    int            `json:"relation_count"`
	HighlightCount   int            `json:"highlight_count"`
	ParticipantCount int            `json:"participant_count"`
	LastLearnedAt    string         `json:"last_learned_at,omitempty"`
	TopAffinity      []affinityView `json:"top_affinity,omitempty"`
}

type memoryProfileView struct {
	SceneSummary        string         `json:"scene_summary,omitempty"`
	Summary             string         `json:"summary,omitempty"`
	StyleSummary        string         `json:"style_summary,omitempty"`
	SlangSummary        string         `json:"slang_summary,omitempty"`
	RelationshipSummary string         `json:"relationship_summary,omitempty"`
	IntentSummary       string         `json:"intent_summary,omitempty"`
	MoodSummary         string         `json:"mood_summary,omitempty"`
	PersonaPrompt       string         `json:"persona_prompt,omitempty"`
	LastLearnedAt       string         `json:"last_learned_at,omitempty"`
	LastError           string         `json:"last_error,omitempty"`
	Highlights          []string       `json:"highlights,omitempty"`
	Relations           []relationEdge `json:"relations,omitempty"`
	Affinity            []affinityView `json:"affinity,omitempty"`
}

type memoryEpisodeView struct {
	ID             string   `json:"id"`
	StartedAt      string   `json:"started_at,omitempty"`
	EndedAt        string   `json:"ended_at,omitempty"`
	SceneSummary   string   `json:"scene_summary,omitempty"`
	Summary        string   `json:"summary,omitempty"`
	Highlights     []string `json:"highlights,omitempty"`
	ParticipantIDs []string `json:"participant_ids,omitempty"`
}

type memoryCardView struct {
	Kind        string   `json:"kind"`
	SubjectID   string   `json:"subject_id,omitempty"`
	SubjectName string   `json:"subject_name,omitempty"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	Confidence  float64  `json:"confidence,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

type affinityView struct {
	UserID string `json:"user_id"`
	Label  string `json:"label"`
	Score  int    `json:"score"`
}

type memoryPersonaView struct {
	ActiveName      string                   `json:"active_name,omitempty"`
	ActiveOrigin    string                   `json:"active_origin,omitempty"`
	ActiveUpdatedAt string                   `json:"active_updated_at,omitempty"`
	ActivePrompt    string                   `json:"active_prompt,omitempty"`
	AutoActive      bool                     `json:"auto_active"`
	Saved           []pluginapi.PersonaEntry `json:"saved,omitempty"`
}

type memoryEventView struct {
	Role         string `json:"role"`
	Time         string `json:"time,omitempty"`
	AuthorID     string `json:"author_id,omitempty"`
	AuthorLabel  string `json:"author_label"`
	Content      string `json:"content,omitempty"`
	ReplyLabel   string `json:"reply_label,omitempty"`
	ReplyContent string `json:"reply_content,omitempty"`
	ImageCount   int    `json:"image_count,omitempty"`
	MentionedBot bool   `json:"mentioned_bot,omitempty"`
	RepliedToBot bool   `json:"replied_to_bot,omitempty"`
}

type memoryGraphPayload struct {
	Nodes  []memoryGraphNode `json:"nodes,omitempty"`
	Edges  []memoryGraphEdge `json:"edges,omitempty"`
	Legend []string          `json:"legend,omitempty"`
}

type memoryGraphNode struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	RecentCount int    `json:"recent_count,omitempty"`
	Affinity    int    `json:"affinity,omitempty"`
	Target      bool   `json:"target,omitempty"`
}

type memoryGraphEdge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Type     string `json:"type,omitempty"`
	Strength int    `json:"strength,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

type configUpdateRequest struct {
	Enabled       bool     `json:"enabled"`
	BatchSize     int      `json:"batch_size"`
	TargetUserIDs []string `json:"target_user_ids,omitempty"`
}

//go:embed webui/*
var memoryWebAssets embed.FS

func ensureWebSettings(ctx context.Context, host *pluginapi.HostClient) (webSettings, error) {
	settings, err := loadWebSettings(ctx, host)
	if err != nil {
		return webSettings{}, err
	}
	if strings.TrimSpace(settings.AccessToken) != "" {
		return settings, nil
	}
	settings.AccessToken, err = generateAccessToken()
	if err != nil {
		return webSettings{}, err
	}
	if err := saveWebSettings(ctx, host, settings); err != nil {
		return webSettings{}, err
	}
	return settings, nil
}

func loadWebSettings(ctx context.Context, host *pluginapi.HostClient) (webSettings, error) {
	var settings webSettings
	found, _, err := host.RecordsGet(ctx, webSettingsCollection, webSettingsKey, &settings)
	if err != nil {
		return webSettings{}, err
	}
	if !found {
		return webSettings{}, nil
	}
	return settings, nil
}

func saveWebSettings(ctx context.Context, host *pluginapi.HostClient, settings webSettings) error {
	return host.RecordsPut(ctx, webSettingsCollection, webSettingsKey, settings)
}

func generateAccessToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func startMemoryWebRuntime(host *pluginapi.HostClient, settings webSettings) (*memoryWebRuntime, error) {
	listenAddr := normalizeListenAddr(os.Getenv(memoryWebAddrEnv))
	publicBaseURL := normalizeBaseURL(os.Getenv(memoryWebPublicEnv))
	if publicBaseURL == "" {
		publicBaseURL = buildLocalURL(listenAddr)
	}
	runtime := &memoryWebRuntime{
		host:          host,
		listenAddr:    listenAddr,
		publicBaseURL: publicBaseURL,
		accessToken:   strings.TrimSpace(settings.AccessToken),
		startedAt:     time.Now(),
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	runtime.server = &http.Server{
		Handler: runtime.routes(),
	}
	go func() {
		if err := runtime.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			runtime.setError(err.Error())
		}
	}()
	return runtime, nil
}

func (m *memoryWebRuntime) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/bootstrap", m.withAuth(m.handleBootstrap))
	mux.HandleFunc("/api/state", m.withAuth(m.handleState))
	mux.HandleFunc("/api/config", m.withAuth(m.handleConfig))
	mux.HandleFunc("/api/learn", m.withAuth(m.handleLearn))
	mux.HandleFunc("/api/token/rotate", m.withAuth(m.handleRotateToken))
	mux.HandleFunc("/", m.handleStatic)
	return mux
}

func (m *memoryWebRuntime) withAuth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !m.authorize(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "unauthorized",
			})
			return
		}
		next(w, r)
	}
}

func (m *memoryWebRuntime) authorize(r *http.Request) bool {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		token = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	m.mu.RLock()
	expected := strings.TrimSpace(m.accessToken)
	m.mu.RUnlock()
	if expected == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1
}

func (m *memoryWebRuntime) handleStatic(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(memoryWebAssets, "webui")
	if err != nil {
		http.Error(w, "web assets unavailable", http.StatusInternalServerError)
		return
	}
	fileServer := http.FileServer(http.FS(sub))
	path := strings.TrimSpace(r.URL.Path)
	switch path {
	case "", "/":
		file, err := sub.Open("index.html")
		if err != nil {
			http.Error(w, "index unavailable", http.StatusInternalServerError)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, file)
	default:
		fileServer.ServeHTTP(w, r)
	}
}

func (m *memoryWebRuntime) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), webRequestTimeout)
	defer cancel()

	scopes, err := listMemoryScopes(ctx, m.host)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, bootstrapResponse{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Web:         m.Snapshot(),
		Scopes:      scopes,
		Hints: []string{
			"默认地址来自 SELF_LEARNING_WEB_ADDR，默认值为 127.0.0.1:17888。",
			"如果做反向代理，可设置 SELF_LEARNING_WEB_PUBLIC_URL 让面板展示公网入口。",
		},
	})
}

func (m *memoryWebRuntime) handleState(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), webRequestTimeout)
	defer cancel()

	scope, err := resolveRequestedScope(ctx, m.host, strings.TrimSpace(r.URL.Query().Get("scope")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	state, err := buildMemoryScopeState(ctx, m.host, scope, m.Snapshot())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (m *memoryWebRuntime) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webRequestTimeout)
	defer cancel()

	scope, err := resolveRequestedScope(ctx, m.host, strings.TrimSpace(r.URL.Query().Get("scope")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var payload configUpdateRequest
	if err := decodeJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	config := normalizeConfig(learningConfig{
		Enabled:       payload.Enabled,
		BatchSize:     payload.BatchSize,
		TargetUserIDs: payload.TargetUserIDs,
	})
	if err := saveConfig(ctx, m.host, scope, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	state, err := buildMemoryScopeState(ctx, m.host, scope, m.Snapshot())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (m *memoryWebRuntime) handleLearn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webRequestTimeout)
	defer cancel()

	scope, err := resolveRequestedScope(ctx, m.host, strings.TrimSpace(r.URL.Query().Get("scope")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if _, _, err := learnScope(ctx, m.host, scope, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	state, err := buildMemoryScopeState(ctx, m.host, scope, m.Snapshot())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (m *memoryWebRuntime) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webRequestTimeout)
	defer cancel()

	settings, err := loadWebSettings(ctx, m.host)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	settings.AccessToken, err = generateAccessToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := saveWebSettings(ctx, m.host, settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	m.UpdateToken(settings.AccessToken)
	writeJSON(w, http.StatusOK, m.Snapshot())
}

func (m *memoryWebRuntime) ListenAddr() string {
	return m.listenAddr
}

func (m *memoryWebRuntime) Close(ctx context.Context) error {
	if ctx == nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), webServerCloseWait)
		defer cancel()
		ctx = closeCtx
	}
	return m.server.Shutdown(ctx)
}

func (m *memoryWebRuntime) UpdateToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accessToken = strings.TrimSpace(token)
}

func (m *memoryWebRuntime) Snapshot() memoryWebStatus {
	m.mu.RLock()
	token := strings.TrimSpace(m.accessToken)
	lastError := strings.TrimSpace(m.lastError)
	m.mu.RUnlock()

	localURL := buildLocalURL(m.listenAddr)
	publicURL := strings.TrimSpace(m.publicBaseURL)
	if publicURL == "" {
		publicURL = localURL
	}
	accessURL := ""
	if token != "" && publicURL != "" {
		accessURL = publicURL + "/?token=" + token
	}

	return memoryWebStatus{
		Running:      lastError == "",
		ListenAddr:   m.listenAddr,
		LocalURL:     localURL,
		PublicURL:    publicURL,
		AccessURL:    accessURL,
		TokenPreview: maskToken(token),
		StartedAt:    m.startedAt.Format(time.RFC3339),
		Error:        lastError,
	}
}

func (m *memoryWebRuntime) setError(message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastError = strings.TrimSpace(message)
}

func listMemoryScopes(ctx context.Context, host *pluginapi.HostClient) ([]memoryScopeItem, error) {
	keys := map[string]struct{}{}
	for _, collection := range []string{configCollection, profileCollection, legacyProfileCollection} {
		cursor := ""
		for {
			response, err := host.RecordsList(ctx, pluginapi.RecordsListRequest{
				Collection: collection,
				Limit:      maxScopeOptions,
				Cursor:     cursor,
			})
			if err != nil {
				return nil, err
			}
			for _, item := range response.Items {
				key := strings.TrimSpace(item.Key)
				if key == "" {
					continue
				}
				keys[key] = struct{}{}
			}
			if strings.TrimSpace(response.NextCursor) == "" {
				break
			}
			cursor = response.NextCursor
		}
	}

	items := make([]memoryScopeItem, 0, len(keys))
	for key := range keys {
		scope := parseScopeKey(key)
		config, err := loadConfig(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		profile, err := loadProfile(ctx, host, scope)
		if err != nil {
			return nil, err
		}
		items = append(items, memoryScopeItem{
			Key:            scopeKey(scope),
			Label:          scopeLabel(scope),
			Enabled:        config.Enabled,
			LastLearnedAt:  profile.LastLearnedAt,
			EventCount:     profile.EventCount,
			EpisodeCount:   profile.EpisodeCount,
			CardCount:      profile.CardCount,
			TargetCount:    len(config.TargetUserIDs),
			RelationCount:  len(profile.Relations),
			HighlightCount: len(profile.Highlights),
		})
	}

	sort.Slice(items, func(i, j int) bool {
		left := strings.TrimSpace(items[i].LastLearnedAt)
		right := strings.TrimSpace(items[j].LastLearnedAt)
		switch {
		case left == right:
			return items[i].Label < items[j].Label
		case left == "":
			return false
		case right == "":
			return true
		default:
			return left > right
		}
	})
	return items, nil
}

func resolveRequestedScope(ctx context.Context, host *pluginapi.HostClient, requested string) (pluginapi.PersonaScope, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		return parseScopeKey(requested), nil
	}
	scopes, err := listMemoryScopes(ctx, host)
	if err != nil {
		return pluginapi.PersonaScope{}, err
	}
	if len(scopes) == 0 {
		return pluginapi.PersonaScope{Type: pluginapi.PersonaScopeGlobal}, nil
	}
	return parseScopeKey(scopes[0].Key), nil
}

func buildMemoryScopeState(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope, web memoryWebStatus) (*memoryScopeStateResponse, error) {
	config, err := loadConfig(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	profile, err := loadProfile(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	recentEvents, err := loadRecentEvents(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	episodes, err := listMemoryEpisodes(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	cards, err := listMemoryCards(ctx, host, scope)
	if err != nil {
		return nil, err
	}
	personaView, _ := loadPersonaView(ctx, host, scope)

	item := memoryScopeItem{
		Key:            scopeKey(scope),
		Label:          scopeLabel(scope),
		Enabled:        config.Enabled,
		LastLearnedAt:  profile.LastLearnedAt,
		EventCount:     profile.EventCount,
		EpisodeCount:   profile.EpisodeCount,
		CardCount:      profile.CardCount,
		TargetCount:    len(config.TargetUserIDs),
		RelationCount:  len(profile.Relations),
		HighlightCount: len(profile.Highlights),
	}

	return &memoryScopeStateResponse{
		Scope:  item,
		Web:    web,
		Config: config,
		Metrics: memoryScopeMetrics{
			Enabled:          config.Enabled,
			EventCount:       profile.EventCount,
			EpisodeCount:     profile.EpisodeCount,
			CardCount:        profile.CardCount,
			TargetCount:      len(config.TargetUserIDs),
			RelationCount:    len(profile.Relations),
			HighlightCount:   len(profile.Highlights),
			ParticipantCount: countParticipants(profile, recentEvents.Events),
			LastLearnedAt:    profile.LastLearnedAt,
			TopAffinity:      buildAffinityViews(profile.Affinity, recentEvents.Events),
		},
		Profile: memoryProfileView{
			SceneSummary:        profile.SceneSummary,
			Summary:             profile.Summary,
			StyleSummary:        profile.StyleSummary,
			SlangSummary:        profile.SlangSummary,
			RelationshipSummary: profile.RelationshipSummary,
			IntentSummary:       profile.IntentSummary,
			MoodSummary:         profile.MoodSummary,
			PersonaPrompt:       profile.PersonaPrompt,
			LastLearnedAt:       profile.LastLearnedAt,
			LastError:           profile.LastError,
			Highlights:          append([]string(nil), profile.Highlights...),
			Relations:           append([]relationEdge(nil), profile.Relations...),
			Affinity:            buildAffinityViews(profile.Affinity, recentEvents.Events),
		},
		Persona:      personaView,
		RecentEvents: buildEventViews(recentEvents.Events),
		Episodes:     buildEpisodeViews(episodes),
		Cards:        buildCardViews(cards),
		Budget:       memoryBudgetPreset(),
		Graph:        buildMemoryGraph(profile, config, recentEvents.Events),
	}, nil
}

func loadPersonaView(ctx context.Context, host *pluginapi.HostClient, scope pluginapi.PersonaScope) (memoryPersonaView, error) {
	view := memoryPersonaView{}
	active, err := host.PersonaGetActive(ctx, scope)
	if err == nil && active != nil && active.Found {
		view.ActiveName = active.Persona.Name
		view.ActiveOrigin = active.Persona.Origin
		view.ActiveUpdatedAt = active.Persona.UpdatedAt
		view.ActivePrompt = active.Persona.Prompt
		view.AutoActive = strings.TrimSpace(active.Persona.Name) == autoPersonaName
	}
	list, err := host.PersonaList(ctx, scope)
	if err != nil || list == nil {
		return view, err
	}
	view.Saved = append([]pluginapi.PersonaEntry(nil), list.Personas...)
	sort.Slice(view.Saved, func(i, j int) bool {
		return view.Saved[i].Name < view.Saved[j].Name
	})
	if len(view.Saved) > maxPersonaPreview {
		view.Saved = view.Saved[:maxPersonaPreview]
	}
	return view, nil
}

func buildAffinityViews(values map[string]int, recentEvents []conversationEvent) []affinityView {
	labels := collectUserLabels(recentEvents)
	items := make([]affinityView, 0, len(values))
	for userID, score := range normalizeAffinity(values) {
		items = append(items, affinityView{
			UserID: userID,
			Label:  firstNonEmpty(labels[userID], userID),
			Score:  score,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return items[i].Label < items[j].Label
		}
		return items[i].Score > items[j].Score
	})
	if len(items) > maxTopAffinities {
		items = items[:maxTopAffinities]
	}
	return items
}

func buildEventViews(events []conversationEvent) []memoryEventView {
	views := make([]memoryEventView, 0, len(events))
	for _, event := range events {
		view := memoryEventView{
			Role:         firstNonEmpty(event.Role, "user"),
			Time:         event.Time,
			AuthorID:     event.Author.ID,
			AuthorLabel:  authorLabel(event.Author),
			Content:      event.Content,
			ImageCount:   len(event.Images),
			MentionedBot: event.MentionedBot,
			RepliedToBot: event.RepliedToBot,
		}
		if event.ReplyTo != nil {
			view.ReplyLabel = authorLabel(event.ReplyTo.Author)
			view.ReplyContent = event.ReplyTo.Content
		}
		views = append(views, view)
	}
	return views
}

func buildEpisodeViews(items []memoryEpisodeRecord) []memoryEpisodeView {
	if len(items) > 6 {
		items = items[:6]
	}
	views := make([]memoryEpisodeView, 0, len(items))
	for _, item := range items {
		views = append(views, memoryEpisodeView{
			ID:             item.ID,
			StartedAt:      item.StartedAt,
			EndedAt:        item.EndedAt,
			SceneSummary:   item.SceneSummary,
			Summary:        item.Summary,
			Highlights:     append([]string(nil), item.Highlights...),
			ParticipantIDs: append([]string(nil), item.ParticipantIDs...),
		})
	}
	return views
}

func buildCardViews(items []memoryCardRecord) []memoryCardView {
	if len(items) > 10 {
		items = items[:10]
	}
	views := make([]memoryCardView, 0, len(items))
	for _, item := range items {
		views = append(views, memoryCardView{
			Kind:        item.Kind,
			SubjectID:   item.SubjectID,
			SubjectName: item.SubjectName,
			Title:       item.Title,
			Content:     item.Content,
			Confidence:  item.Confidence,
			Evidence:    append([]string(nil), item.Evidence...),
			UpdatedAt:   item.UpdatedAt,
		})
	}
	return views
}

func buildMemoryGraph(profile learningProfile, config learningConfig, recentEvents []conversationEvent) memoryGraphPayload {
	labels := collectUserLabels(recentEvents)
	recentCounts := map[string]int{}
	for _, event := range recentEvents {
		if userID := strings.TrimSpace(event.Author.ID); userID != "" {
			recentCounts[userID]++
		}
	}

	targets := map[string]struct{}{}
	for _, userID := range config.TargetUserIDs {
		targets[strings.TrimSpace(userID)] = struct{}{}
	}

	nodes := map[string]memoryGraphNode{}
	legendSet := map[string]struct{}{}
	edges := make([]memoryGraphEdge, 0, len(profile.Relations))

	for userID, score := range normalizeAffinity(profile.Affinity) {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		nodes[userID] = memoryGraphNode{
			ID:          userID,
			Label:       firstNonEmpty(labels[userID], userID),
			RecentCount: recentCounts[userID],
			Affinity:    score,
			Target:      hasUser(targets, userID),
		}
	}

	for _, relation := range profile.Relations {
		left := strings.TrimSpace(relation.LeftUserID)
		right := strings.TrimSpace(relation.RightUserID)
		if left == "" || right == "" {
			continue
		}
		if _, ok := nodes[left]; !ok {
			nodes[left] = memoryGraphNode{
				ID:          left,
				Label:       firstNonEmpty(labels[left], left),
				RecentCount: recentCounts[left],
				Affinity:    normalizeAffinity(profile.Affinity)[left],
				Target:      hasUser(targets, left),
			}
		}
		if _, ok := nodes[right]; !ok {
			nodes[right] = memoryGraphNode{
				ID:          right,
				Label:       firstNonEmpty(labels[right], right),
				RecentCount: recentCounts[right],
				Affinity:    normalizeAffinity(profile.Affinity)[right],
				Target:      hasUser(targets, right),
			}
		}
		edgeType := firstNonEmpty(relation.Type, "未标注")
		legendSet[edgeType] = struct{}{}
		edges = append(edges, memoryGraphEdge{
			Source:   left,
			Target:   right,
			Type:     edgeType,
			Strength: relation.Strength,
			Evidence: relation.Evidence,
		})
	}

	nodeList := make([]memoryGraphNode, 0, len(nodes))
	for _, node := range nodes {
		nodeList = append(nodeList, node)
	}
	sort.Slice(nodeList, func(i, j int) bool {
		if nodeList[i].RecentCount == nodeList[j].RecentCount {
			return nodeList[i].Label < nodeList[j].Label
		}
		return nodeList[i].RecentCount > nodeList[j].RecentCount
	})

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Strength == edges[j].Strength {
			return edges[i].Type < edges[j].Type
		}
		return edges[i].Strength > edges[j].Strength
	})

	legend := make([]string, 0, len(legendSet))
	for value := range legendSet {
		legend = append(legend, value)
	}
	sort.Strings(legend)

	return memoryGraphPayload{
		Nodes:  nodeList,
		Edges:  edges,
		Legend: legend,
	}
}

func countParticipants(profile learningProfile, events []conversationEvent) int {
	seen := map[string]struct{}{}
	for userID := range profile.Affinity {
		userID = strings.TrimSpace(userID)
		if userID != "" {
			seen[userID] = struct{}{}
		}
	}
	for _, relation := range profile.Relations {
		if userID := strings.TrimSpace(relation.LeftUserID); userID != "" {
			seen[userID] = struct{}{}
		}
		if userID := strings.TrimSpace(relation.RightUserID); userID != "" {
			seen[userID] = struct{}{}
		}
	}
	for _, event := range events {
		if userID := strings.TrimSpace(event.Author.ID); userID != "" {
			seen[userID] = struct{}{}
		}
		if event.ReplyTo != nil {
			if userID := strings.TrimSpace(event.ReplyTo.Author.ID); userID != "" {
				seen[userID] = struct{}{}
			}
		}
	}
	return len(seen)
}

func collectUserLabels(events []conversationEvent) map[string]string {
	labels := map[string]string{}
	for _, event := range events {
		if userID := strings.TrimSpace(event.Author.ID); userID != "" {
			labels[userID] = authorLabel(event.Author)
		}
		if event.ReplyTo != nil {
			if userID := strings.TrimSpace(event.ReplyTo.Author.ID); userID != "" {
				labels[userID] = authorLabel(event.ReplyTo.Author)
			}
		}
	}
	return labels
}

func normalizeListenAddr(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultMemoryWebAddr
	}
	return value
}

func normalizeBaseURL(value string) string {
	value = strings.TrimSpace(strings.TrimRight(value, "/"))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	return "http://" + value
}

func buildLocalURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return "http://" + strings.TrimSpace(listenAddr)
	}
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func decodeJSONBody(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func maskToken(token string) string {
	token = strings.TrimSpace(token)
	switch {
	case token == "":
		return ""
	case len(token) <= 10:
		return token
	default:
		return token[:6] + "..." + token[len(token)-4:]
	}
}

func webPanelPreview(web memoryWebStatus, isAdmin bool) string {
	lines := []string{
		"状态: " + map[bool]string{true: "运行中", false: "未启动"}[web.Running],
		"监听地址: " + firstNonEmpty(web.ListenAddr, "未配置"),
	}
	if strings.TrimSpace(web.Error) != "" {
		lines = append(lines, "错误: "+web.Error)
	}
	if isAdmin {
		lines = append(lines, "本地入口: "+firstNonEmpty(web.LocalURL, "不可用"))
		lines = append(lines, "外部入口: "+firstNonEmpty(web.PublicURL, web.LocalURL))
		lines = append(lines, "访问令牌: "+firstNonEmpty(web.TokenPreview, "未生成"))
		if strings.TrimSpace(web.AccessURL) != "" {
			lines = append(lines, "完整链接: "+web.AccessURL)
		}
		lines = append(lines, "环境变量: SELF_LEARNING_WEB_ADDR / SELF_LEARNING_WEB_PUBLIC_URL")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "完整入口和令牌仅管理员可见。")
	return strings.Join(lines, "\n")
}

func hasUser(values map[string]struct{}, userID string) bool {
	_, ok := values[strings.TrimSpace(userID)]
	return ok
}
