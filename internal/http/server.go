package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"mind-the-time/internal/app"
	"mind-the-time/internal/domain"
)

const (
	createFormMaxBytes     int64 = 64 * 1024
	evaluationFormMaxBytes int64 = 1024
	ownerCookieLifetime          = 90 * 24 * time.Hour
)

var (
	errFormTooLarge   = errors.New("form too large")
	errInvalidWeekday = errors.New("invalid weekday")
)

const defaultPageTemplate = `{{define "page"}}<!doctype html><html lang="fr"><head><meta charset="utf-8"><title>Mind the Time</title></head><body><h1>Mind the Time</h1>{{if not .ProviderAvailable}}<p>L'évaluation PRIM est indisponible dans cette version.</p>{{end}}{{if .Error}}<p>{{.Error}}</p>{{end}}<form method="post" action="{{.FormAction}}"><label>Origine <input name="origin" value="{{.Input.Origin}}"></label><label>Destination <input name="destination" value="{{.Input.Destination}}"></label><label>Heure de départ <input name="departure_time" value="{{.Input.DepartureTime}}"></label><label><input type="checkbox" name="weekday" value="monday" {{if index .Weekdays "monday"}}checked{{end}}>Lundi</label><label><input type="checkbox" name="weekday" value="tuesday" {{if index .Weekdays "tuesday"}}checked{{end}}>Mardi</label><label><input type="checkbox" name="weekday" value="wednesday" {{if index .Weekdays "wednesday"}}checked{{end}}>Mercredi</label><label><input type="checkbox" name="weekday" value="thursday" {{if index .Weekdays "thursday"}}checked{{end}}>Jeudi</label><label><input type="checkbox" name="weekday" value="friday" {{if index .Weekdays "friday"}}checked{{end}}>Vendredi</label><label><input type="checkbox" name="weekday" value="saturday" {{if index .Weekdays "saturday"}}checked{{end}}>Samedi</label><label><input type="checkbox" name="weekday" value="sunday" {{if index .Weekdays "sunday"}}checked{{end}}>Dimanche</label><button type="submit">Enregistrer le trajet</button></form>{{if .CommuteID}}<form method="post" action="/commutes/{{.CommuteID}}/evaluate"><button type="submit">Évaluer</button></form><form method="post" action="/commutes/{{.CommuteID}}/explore"><button type="submit">Explorer</button></form><form method="post" action="/commutes/{{.CommuteID}}/save-combinations"><button type="submit">Sauvegarder</button></form><form method="post" action="/commutes/{{.CommuteID}}/plan"><button type="submit">Planifier</button></form>{{end}}{{if .Routes}}<p>{{len .Routes}} itinéraires admissibles.</p>{{end}}</body></html>{{end}}`

type Dependencies struct {
	Owners            app.OwnerStore
	Commutes          app.CommuteStore
	Routes            app.RouteService
	Planner           DeparturePlanner
	CookieName        string
	CookieSecure      bool
	ProviderAvailable bool
	Now               func() time.Time
}

// A nil planner is the off switch: the page then behaves exactly as before.
type DeparturePlanner interface {
	Plan(context.Context, app.PlanRequest) ([]domain.DepartureOption, error)
}

type departureOptionDisplay struct {
	LeaveAt   string
	Arrival   string
	Duration  string
	IsFastest bool
	IsLatest  bool
}

type Server struct {
	templates    *template.Template
	templateName string
	deps         Dependencies
	handler      http.Handler
	location     *time.Location
}

type ownerContextKey struct{}

type routeOptionDisplay struct {
	Combination        string
	RecommendedTime    string
	AlternativeTime    string
	RouteLegs          []routeLegDisplay
	IsSelected         bool
	InterchangeWarning string
}

type pageData struct {
	Input                      app.CommuteInput
	Weekdays                   map[string]bool
	CommuteID                  app.CommuteID
	FormAction                 string
	ProviderAvailable          bool
	Error                      string
	Origin                     string
	Destination                string
	DepartureTime              string
	Days                       map[string]bool
	Routes                     []domain.Itinerary
	HasResult                  bool
	ProviderUnavailable        bool
	RouteOptions               []routeOptionDisplay
	ID                         app.CommuteID
	Name                       string
	Commutes                   []commuteListItem
	EvalDate                   string
	EvalTime                   string
	MaxInitialWalkMinutes      string
	SaintLazareTransferMinutes string
	PreferredLines             string
	HasSelection               bool
	SelectedCombination        string
	DepartureOptions           []departureOptionDisplay
	WindowStart                string
	WindowEnd                  string

	ExploredCandidates []discoveredCandidateDisplay
	HasExploredResult  bool
	SavedCombinations  []savedCombinationDisplay
	PlannedResults     []plannedResultDisplay
	HasPlannedResult   bool
	ExplorationDate    string
	ExplorationStart   string
	ExplorationEnd     string
	PlanningDate       string
	PlanningStart      string
	PlanningEnd        string
}

type discoveredCandidateDisplay struct {
	Fingerprint string
	Display     string
	Legs        []routeLegDisplay
}

type savedCombinationDisplay struct {
	ID          string
	Combination string
	Legs        []routeLegDisplay
}

type plannedResultDisplay struct {
	ID          string
	Combination string
	Outcome     string
	OutcomeType string
	Options     []departureOptionDisplay
	IsBest      bool
}

type commuteListItem struct {
	ID   app.CommuteID
	Name string
}

type routeLegDisplay struct {
	IsWalking    bool
	IsTransit    bool
	From         string
	To           string
	TimeRange    string
	Duration     string
	DurationMins int
	Label        string
}

func NewServer(assets fs.FS, dependencies Dependencies) (*Server, error) {
	if assets == nil {
		return nil, fmt.Errorf("template filesystem is required")
	}
	if dependencies.Owners == nil || dependencies.Commutes == nil {
		return nil, fmt.Errorf("owner and commute stores are required")
	}
	if dependencies.CookieName == "" {
		return nil, fmt.Errorf("cookie name is required")
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	templates, templateName, err := parseTemplates(assets)
	if err != nil {
		return nil, err
	}
	staticAssets, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("create static asset filesystem: %w", err)
	}
	location, err := domain.NetworkLocation()
	if err != nil {
		return nil, fmt.Errorf("load network timezone: %w", err)
	}
	server := &Server{templates: templates, templateName: templateName, deps: dependencies, location: location}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.healthz)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticAssets)))
	swHandler := http.FileServerFS(staticAssets)
	mux.HandleFunc("GET /sw.js", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-cache")
		swHandler.ServeHTTP(writer, request)
	})
	mux.Handle("GET /{$}", server.withOwner(true, http.HandlerFunc(server.index)))
	mux.Handle("POST /commutes", server.withOwner(true, http.HandlerFunc(server.createCommute)))
	mux.Handle("GET /commutes/{commuteID}", server.withOwner(false, http.HandlerFunc(server.showCommuteRequest)))
	mux.Handle("POST /commutes/{commuteID}", server.withOwner(false, http.HandlerFunc(server.updateCommuteRequest)))
	mux.Handle("POST /commutes/{commuteID}/delete", server.withOwner(false, http.HandlerFunc(server.deleteCommuteRequest)))
	mux.Handle("POST /commutes/{commuteID}/evaluate", server.withOwner(false, http.HandlerFunc(server.evaluateCommuteRequest)))
	mux.Handle("POST /commutes/{commuteID}/explore", server.withOwner(false, http.HandlerFunc(server.exploreCommuteRequest)))
	mux.Handle("POST /commutes/{commuteID}/save-combinations", server.withOwner(false, http.HandlerFunc(server.saveCombinationsRequest)))
	mux.Handle("POST /commutes/{commuteID}/plan", server.withOwner(false, http.HandlerFunc(server.planDeparturesRequest)))
	mux.HandleFunc("/", server.notFound)
	server.handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers := writer.Header()
		headers.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")
		headers.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(request.URL.Path, "/commutes/") && strings.Contains(request.URL.Path, "//") {
			server.notFound(writer, request)
			return
		}
		mux.ServeHTTP(writer, request)
	})
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func parseTemplates(assets fs.FS) (*template.Template, string, error) {
	templates, err := template.New("page").Parse(defaultPageTemplate)
	if err != nil {
		return nil, "", fmt.Errorf("parse default template: %w", err)
	}
	var paths []string
	err = fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && path.Ext(name) == ".html" {
			paths = append(paths, name)
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("walk template filesystem: %w", err)
	}
	if len(paths) == 0 {
		return templates, "page", nil
	}
	templates, err = templates.ParseFS(assets, paths...)
	if err != nil {
		return nil, "", fmt.Errorf("parse UI templates: %w", err)
	}
	if templates.Lookup("index.html") != nil {
		return templates, "index.html", nil
	}
	return templates, "page", nil
}

func (s *Server) healthz(writer http.ResponseWriter, request *http.Request) {
	if request.Context().Err() != nil {
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := writer.Write([]byte("ok\n")); err != nil {
		return
	}
}

func (s *Server) withOwner(create bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		ownerID, err := s.resolveOwner(request.Context(), request)
		if err != nil {
			http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
			return
		}
		if ownerID == "" && create {
			ownerID, err = s.deps.Owners.Create(request.Context())
			if errors.Is(err, app.ErrOwnerCapacityExceeded) {
				http.Error(writer, "La capacité de stockage est temporairement atteinte.", http.StatusServiceUnavailable)
				return
			}
			if err != nil {
				http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
				return
			}
		}
		if ownerID != "" {
			s.setOwnerCookie(writer, ownerID)
		}
		request = request.WithContext(context.WithValue(request.Context(), ownerContextKey{}, ownerID))
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) setOwnerCookie(writer http.ResponseWriter, ownerID app.OwnerID) {
	http.SetCookie(writer, &http.Cookie{
		Name:     s.deps.CookieName,
		Value:    string(ownerID),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.deps.CookieSecure,
		MaxAge:   int(ownerCookieLifetime / time.Second),
		Expires:  s.deps.Now().Add(ownerCookieLifetime),
	})
}

func (s *Server) resolveOwner(ctx context.Context, request *http.Request) (app.OwnerID, error) {
	cookie, err := request.Cookie(s.deps.CookieName)
	if errors.Is(err, http.ErrNoCookie) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read owner cookie: %w", err)
	}
	ownerID := app.OwnerID(cookie.Value)
	renewed, err := s.deps.Owners.Touch(ctx, ownerID)
	if err != nil {
		return "", fmt.Errorf("touch owner: %w", err)
	}
	if !renewed {
		return "", nil
	}
	return ownerID, nil
}

func (s *Server) index(writer http.ResponseWriter, request *http.Request) {
	commutes, err := s.deps.Commutes.List(request.Context(), ownerIDFromContext(request.Context()))
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	data := newPageData(app.CommuteInput{}, "", s.deps.ProviderAvailable, s.deps.Now())
	data.Commutes = commuteListItems(commutes)
	s.renderPage(writer, request, http.StatusOK, data)
}

func (s *Server) notFound(writer http.ResponseWriter, _ *http.Request) {
	http.Error(writer, "Page introuvable.", http.StatusNotFound)
}

func mapValidationError(err error) string {
	var valErr *app.ValidationError
	if errors.As(err, &valErr) {
		switch valErr.Field {
		case app.FieldName:
			return "Le nom du trajet est invalide."
		case app.FieldOrigin:
			return "Le point de départ est invalide."
		case app.FieldDestination:
			return "La destination est invalide."
		case app.FieldDepartureTime:
			return "L'heure de départ est invalide."
		case app.FieldWeekdays:
			return "Sélectionnez au moins un jour actif."
		case app.FieldMaximumInitialWalk:
			return "La marche initiale maximale doit être un nombre positif de minutes."
		case app.FieldSaintLazareTransfer:
			return "Le temps de correspondance à Saint-Lazare doit être un nombre positif de minutes."
		}
	}
	return "Les informations du trajet sont invalides."
}

func (s *Server) createCommute(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, createFormMaxBytes)
	input, err := inputFromRequest(request)
	if errors.Is(err, errFormTooLarge) {
		data := newPageData(input, "", s.deps.ProviderAvailable, s.deps.Now())
		data.Error = "La taille du formulaire dépasse la limite autorisée."
		s.renderPage(writer, request, http.StatusRequestEntityTooLarge, data)
		return
	}
	if errors.Is(err, errInvalidWeekday) {
		data := newPageData(input, "", s.deps.ProviderAvailable, s.deps.Now())
		data.Error = "Sélectionnez au moins un jour actif."
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}
	if err != nil {
		data := newPageData(input, "", s.deps.ProviderAvailable, s.deps.Now())
		data.Error = "Les informations du trajet sont invalides."
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}
	name, commute, err := app.NewCommute(input)
	if errors.Is(err, app.ErrInvalidInput) {
		data := newPageData(input, "", s.deps.ProviderAvailable, s.deps.Now())
		data.Error = mapValidationError(err)
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	record, err := s.deps.Commutes.Create(request.Context(), ownerIDFromContext(request.Context()), name, commute)
	if errors.Is(err, app.ErrCommuteCapacityExceeded) {
		http.Error(writer, "La capacité de stockage est temporairement atteinte.", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, "/commutes/"+string(record.ID), http.StatusSeeOther)
}

func (s *Server) showCommuteRequest(writer http.ResponseWriter, request *http.Request) {
	s.showCommute(writer, request, app.CommuteID(request.PathValue("commuteID")))
}

func (s *Server) evaluateCommuteRequest(writer http.ResponseWriter, request *http.Request) {
	s.evaluateCommute(writer, request, app.CommuteID(request.PathValue("commuteID")))
}

func (s *Server) updateCommuteRequest(writer http.ResponseWriter, request *http.Request) {
	s.updateCommute(writer, request, app.CommuteID(request.PathValue("commuteID")))
}

func (s *Server) deleteCommuteRequest(writer http.ResponseWriter, request *http.Request) {
	commuteID := app.CommuteID(request.PathValue("commuteID"))
	err := s.deps.Commutes.Delete(request.Context(), ownerIDFromContext(request.Context()), commuteID)
	if errors.Is(err, app.ErrCommuteNotFound) {
		s.notFound(writer, request)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, "/", http.StatusSeeOther)
}

func (s *Server) showCommute(writer http.ResponseWriter, request *http.Request, commuteID app.CommuteID) {
	record, err := s.deps.Commutes.Get(request.Context(), ownerIDFromContext(request.Context()), commuteID)
	if errors.Is(err, app.ErrCommuteNotFound) {
		s.notFound(writer, request)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	s.renderPage(writer, request, http.StatusOK, newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now()))
}

func (s *Server) updateCommute(writer http.ResponseWriter, request *http.Request, commuteID app.CommuteID) {
	if _, err := s.deps.Commutes.Get(request.Context(), ownerIDFromContext(request.Context()), commuteID); errors.Is(err, app.ErrCommuteNotFound) {
		s.notFound(writer, request)
		return
	} else if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, createFormMaxBytes)
	input, err := inputFromRequest(request)
	if errors.Is(err, errFormTooLarge) {
		s.renderPage(writer, request, http.StatusRequestEntityTooLarge, inputErrorPageData(input, commuteID, s.deps.ProviderAvailable, "La taille du formulaire dépasse la limite autorisée.", s.deps.Now()))
		return
	}
	if errors.Is(err, errInvalidWeekday) {
		s.renderPage(writer, request, http.StatusBadRequest, inputErrorPageData(input, commuteID, s.deps.ProviderAvailable, "Sélectionnez au moins un jour actif.", s.deps.Now()))
		return
	}
	if err != nil {
		s.renderPage(writer, request, http.StatusBadRequest, inputErrorPageData(input, commuteID, s.deps.ProviderAvailable, "Les informations du trajet sont invalides.", s.deps.Now()))
		return
	}
	name, commute, err := app.NewCommute(input)
	if errors.Is(err, app.ErrInvalidInput) {
		s.renderPage(writer, request, http.StatusBadRequest, inputErrorPageData(input, commuteID, s.deps.ProviderAvailable, mapValidationError(err), s.deps.Now()))
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	record, err := s.deps.Commutes.Update(request.Context(), ownerIDFromContext(request.Context()), commuteID, name, commute)
	if errors.Is(err, app.ErrCommuteNotFound) {
		s.notFound(writer, request)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, "/commutes/"+string(record.ID), http.StatusSeeOther)
}

func (s *Server) evaluateCommute(writer http.ResponseWriter, request *http.Request, commuteID app.CommuteID) {
	record, err := s.deps.Commutes.Get(request.Context(), ownerIDFromContext(request.Context()), commuteID)
	if errors.Is(err, app.ErrCommuteNotFound) {
		s.notFound(writer, request)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, evaluationFormMaxBytes)
	serviceDate, err := serviceDateFromRequest(request, record.Commute.Schedule(), s.deps.Now(), s.location)
	if errors.Is(err, errFormTooLarge) {
		s.renderPage(writer, request, http.StatusRequestEntityTooLarge, errorPageData(record, s.deps.ProviderAvailable, "La taille du formulaire dépasse la limite autorisée.", s.deps.Now()))
		return
	}
	if err != nil {
		s.renderPage(writer, request, http.StatusBadRequest, errorPageData(record, s.deps.ProviderAvailable, "La date de service est invalide.", s.deps.Now()))
		return
	}
	if !record.Commute.Schedule().IsActive(serviceDate.Weekday()) {
		s.renderPage(writer, request, http.StatusBadRequest, errorPageData(record, s.deps.ProviderAvailable, "Le trajet n’est pas actif à cette date.", s.deps.Now()))
		return
	}
	evalTimeStr := request.PostForm.Get("time")
	var departure time.Time
	if strings.TrimSpace(evalTimeStr) != "" {
		parsedTime, parseErr := domain.NewDepartureTime(evalTimeStr)
		if parseErr != nil {
			s.renderPage(writer, request, http.StatusBadRequest, errorPageData(record, s.deps.ProviderAvailable, "L'heure d'évaluation est invalide.", s.deps.Now()))
			return
		}
		departure = time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), parsedTime.Hour(), parsedTime.Minute(), 0, 0, s.location)
	} else {
		departure, err = record.Commute.Schedule().DepartureAt(serviceDate)
		if err != nil {
			http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
			return
		}
	}
	window := resolveSearchWindow(request.PostForm, serviceDate, departure, s.location)
	selectedCombination := strings.TrimSpace(request.PostForm.Get("selected_combination"))

	evalCommute, err := app.CommuteForEvaluation(record.Commute, app.EvaluationOverrides{
		MaximumInitialWalkMinutes:  request.PostForm.Get("maximum_initial_walk"),
		SaintLazareTransferMinutes: request.PostForm.Get("saint_lazare_transfer"),
		SelectedCombination:        selectedCombination,
	})
	if err != nil {
		s.renderPage(writer, request, http.StatusBadRequest, errorPageData(record, s.deps.ProviderAvailable, mapValidationError(err), s.deps.Now()))
		return
	}

	routes, err := s.deps.Routes.Evaluate(request.Context(), app.RouteRequest{Commute: evalCommute, Departure: departure})
	if errors.Is(err, app.ErrRateLimited) {
		s.renderPage(writer, request, http.StatusTooManyRequests, errorPageData(record, s.deps.ProviderAvailable, "L’évaluation des itinéraires est limitée.", s.deps.Now()))
		return
	}
	if errors.Is(err, app.ErrProviderUnavailable) || errors.Is(err, app.ErrProviderUnsupportedContract) {
		s.renderPage(writer, request, http.StatusServiceUnavailable, errorPageData(record, false, "L'évaluation PRIM est indisponible dans cette version.", s.deps.Now()))
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
	data.EvalDate = serviceDate.Format("2006-01-02")
	data.EvalTime = departure.Format("15:04")
	data.Routes = routes
	data.HasResult = len(routes) > 0
	data.HasSelection = (selectedCombination != "")
	data.SelectedCombination = selectedCombination
	if len(routes) == 0 {
		data.Error = noEligibleRouteMessage(selectedCombination)
	}
	var options []routeOptionDisplay
	grouped := make(map[string][]domain.Itinerary)
	var order []string
	for _, route := range routes {
		comb := route.LineCombination()
		if _, exists := grouped[comb]; !exists {
			order = append(order, comb)
		}
		grouped[comb] = append(grouped[comb], route)
	}
	for idx, comb := range order {
		groupRoutes := grouped[comb]
		recTime := leaveAtDisplay(groupRoutes[0], evalCommute)
		altTime := recTime
		if len(groupRoutes) > 1 {
			altTime = leaveAtDisplay(groupRoutes[1], evalCommute)
		}
		isSelected := (selectedCombination == comb) || (selectedCombination == "" && idx == 0)
		options = append(options, routeOptionDisplay{
			Combination:        comb,
			RecommendedTime:    recTime,
			AlternativeTime:    altTime,
			RouteLegs:          buildRouteLegsDisplay(groupRoutes[0]),
			IsSelected:         isSelected,
			InterchangeWarning: interchangeWarning(groupRoutes[0], evalCommute),
		})
	}
	data.RouteOptions = options
	data.WindowStart = window.start.Format("15:04")
	data.WindowEnd = window.end.Format("15:04")
	data.DepartureOptions = s.planDepartures(request.Context(), grouped[selectedCombination], evalCommute, window)

	s.renderPage(writer, request, http.StatusOK, data)
}

const defaultSearchWindowWidth = 30 * time.Minute

// Anchoring the search on the evaluation time alone hides a faster departure a few minutes earlier.
type searchWindow struct {
	start time.Time
	end   time.Time
}

func resolveSearchWindow(form url.Values, serviceDate, departure time.Time, location *time.Location) searchWindow {
	window := searchWindow{start: departure, end: departure.Add(defaultSearchWindowWidth)}
	if start, ok := clockOnDate(form.Get("window_start"), serviceDate, location); ok {
		window.start = start
	}
	if end, ok := clockOnDate(form.Get("window_end"), serviceDate, location); ok {
		window.end = end
	}
	if !window.end.After(window.start) {
		window.end = window.start.Add(defaultSearchWindowWidth)
	}
	return window
}

func clockOnDate(value string, serviceDate time.Time, location *time.Location) (time.Time, bool) {
	parsed, err := domain.NewDepartureTime(strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location), true
}

func (s *Server) planDepartures(
	ctx context.Context,
	selected []domain.Itinerary,
	commute domain.Commute,
	window searchWindow,
) []departureOptionDisplay {
	if s.deps.Planner == nil || len(selected) == 0 {
		return nil
	}
	request, err := app.PlanRequestForSelection(selected, commute, window.start, window.end.Sub(window.start))
	if err != nil {
		log.Printf("departure planning skipped: %v", err)
		return nil
	}
	options, err := s.deps.Planner.Plan(ctx, request)
	if err != nil {
		log.Printf("departure planning failed: %v", err)
		return nil
	}
	return departureOptionDisplays(options)
}

func departureOptionDisplays(options []domain.DepartureOption) []departureOptionDisplay {
	if len(options) == 0 {
		return nil
	}
	latest := 0
	for index, option := range options {
		if option.LeaveAt().After(options[latest].LeaveAt()) {
			latest = index
		}
	}
	displays := make([]departureOptionDisplay, 0, len(options))
	for index, option := range options {
		// Computed from the displayed clock values so a reader can check the arithmetic.
		leaveAt := option.LeaveAt().Truncate(time.Minute)
		arrival := option.Arrival().Truncate(time.Minute)
		displays = append(displays, departureOptionDisplay{
			LeaveAt:   leaveAt.Format("15:04"),
			Arrival:   arrival.Format("15:04"),
			Duration:  fmt.Sprintf("%d min", int(arrival.Sub(leaveAt).Minutes())),
			IsFastest: index == 0,
			IsLatest:  index == latest,
		})
	}
	return displays
}

func leaveAtDisplay(itinerary domain.Itinerary, commute domain.Commute) string {
	return itinerary.LeaveAt(commute.MaximumInitialWalk()).Format("15:04")
}

func interchangeWarning(itinerary domain.Itinerary, commute domain.Commute) string {
	declared := commute.SaintLazareMinimumTransferTime()
	if declared.Duration() <= 0 || itinerary.InterchangeFits(declared) {
		return ""
	}
	tightest, hasTransfer := itinerary.TightestInterchange()
	if !hasTransfer {
		return ""
	}
	return fmt.Sprintf(
		"Correspondance la plus courte : %d min, alors que vous en demandez %d.",
		int(tightest.Minutes()), int(declared.Duration().Minutes()),
	)
}

func noEligibleRouteMessage(selectedCombination string) string {
	if strings.TrimSpace(selectedCombination) == "" {
		return "Aucun itinéraire n’est proposé pour cet horaire."
	}
	return "Ce parcours n’a pas pu être retrouvé pour cet horaire. Réessayez : le réseau ou le fournisseur peut être momentanément indisponible. Sinon, choisissez un autre parcours ou une autre heure."
}

func inputFromRequest(request *http.Request) (app.CommuteInput, error) {
	if err := parseForm(request); err != nil {
		return app.CommuteInput{}, err
	}
	input := app.CommuteInput{
		Name:                       request.PostForm.Get("name"),
		Origin:                     request.PostForm.Get("origin"),
		Destination:                request.PostForm.Get("destination"),
		DepartureTime:              request.PostForm.Get("departure_time"),
		MaximumInitialWalkMinutes:  request.PostForm.Get("maximum_initial_walk"),
		SaintLazareTransferMinutes: request.PostForm.Get("saint_lazare_transfer"),
		PreferredLines:             request.PostForm.Get("preferred_lines"),
	}
	weekdayValues := request.PostForm["weekday"]
	if len(weekdayValues) == 0 {
		weekdayValues = request.PostForm["days"]
	}
	for _, value := range weekdayValues {
		day, ok := weekdayByName(value)
		if !ok {
			return input, errInvalidWeekday
		}
		input.Weekdays = append(input.Weekdays, day)
	}
	return input, nil
}

func serviceDateFromRequest(request *http.Request, schedule domain.WeeklySchedule, now time.Time, loc *time.Location) (time.Time, error) {
	if err := parseForm(request); err != nil {
		return time.Time{}, err
	}
	value := request.PostForm.Get("date")
	if value == "" {
		return schedule.NextActiveDate(now, loc), nil
	}
	return time.ParseInLocation("2006-01-02", value, loc)
}

func parseForm(request *http.Request) error {
	if err := request.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return errFormTooLarge
		}
		return err
	}
	return nil
}

func weekdayByName(value string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sunday":
		return time.Sunday, true
	case "monday":
		return time.Monday, true
	case "tuesday":
		return time.Tuesday, true
	case "wednesday":
		return time.Wednesday, true
	case "thursday":
		return time.Thursday, true
	case "friday":
		return time.Friday, true
	case "saturday":
		return time.Saturday, true
	default:
		return 0, false
	}
}

func networkLocationOrUTC() *time.Location {
	if loc, err := domain.NetworkLocation(); err == nil {
		return loc
	}
	return time.UTC
}

func ownerIDFromContext(ctx context.Context) app.OwnerID {
	ownerID, ok := ctx.Value(ownerContextKey{}).(app.OwnerID)
	if !ok {
		return ""
	}
	return ownerID
}

func newPageData(input app.CommuteInput, commuteID app.CommuteID, providerAvailable bool, now time.Time) pageData {
	formAction := "/commutes"
	if commuteID != "" {
		formAction += "/" + string(commuteID)
	}
	activeDays := weekdays(input)

	now = now.In(networkLocationOrUTC())
	evalDate := now.Format("2006-01-02")
	if len(input.Weekdays) > 0 {
		var activeDaysArr [7]bool
		for _, day := range input.Weekdays {
			if day >= time.Sunday && day <= time.Saturday {
				activeDaysArr[day] = true
			}
		}
		for i := 0; i < 7; i++ {
			candidate := now.AddDate(0, 0, i)
			if activeDaysArr[candidate.Weekday()] {
				evalDate = candidate.Format("2006-01-02")
				break
			}
		}
	}
	evalTime := input.DepartureTime
	if evalTime == "" {
		evalTime = "18:00"
	}

	return pageData{
		Input:                      input,
		Weekdays:                   activeDays,
		CommuteID:                  commuteID,
		FormAction:                 formAction,
		ProviderAvailable:          providerAvailable,
		Origin:                     input.Origin,
		Destination:                input.Destination,
		DepartureTime:              input.DepartureTime,
		Days:                       activeDays,
		ProviderUnavailable:        !providerAvailable,
		ID:                         commuteID,
		Name:                       input.Name,
		MaxInitialWalkMinutes:      input.MaximumInitialWalkMinutes,
		SaintLazareTransferMinutes: input.SaintLazareTransferMinutes,
		PreferredLines:             input.PreferredLines,
		EvalDate:                   evalDate,
		EvalTime:                   evalTime,
	}
}

func inputErrorPageData(input app.CommuteInput, commuteID app.CommuteID, providerAvailable bool, message string, now time.Time) pageData {
	data := newPageData(input, commuteID, providerAvailable, now)
	data.Error = message
	return data
}

func errorPageData(record app.CommuteRecord, providerAvailable bool, message string, now time.Time) pageData {
	data := newPageData(app.InputFromCommuteRecord(record), record.ID, providerAvailable, now)
	data.Error = message
	return data
}

func commuteListItems(records []app.CommuteRecord) []commuteListItem {
	items := make([]commuteListItem, 0, len(records))
	for _, record := range records {
		items = append(items, commuteListItem{ID: record.ID, Name: record.Name.Text()})
	}
	return items
}

func buildRouteLegsDisplay(itinerary domain.Itinerary) []routeLegDisplay {
	var displays []routeLegDisplay
	for _, leg := range itinerary.Legs() {
		durMinutes := int(leg.Duration().Round(time.Minute).Minutes())
		if durMinutes < 1 {
			durMinutes = 1
		}
		timeRange := fmt.Sprintf("%s - %s", leg.Departure().Format("15:04"), leg.Arrival().Format("15:04"))
		if leg.Mode() == "walking" {
			displays = append(displays, routeLegDisplay{
				IsWalking:    true,
				Duration:     fmt.Sprintf("%d min", durMinutes),
				DurationMins: durMinutes,
				TimeRange:    timeRange,
			})
		} else {
			var labelStr string
			if label, ok := leg.TransitLabel(); ok {
				var parts []string
				if label.Network() != "" {
					parts = append(parts, label.Network())
				}
				if label.Code() != "" {
					parts = append(parts, label.Code())
				}
				labelStr = strings.Join(parts, " ")
			}
			displays = append(displays, routeLegDisplay{
				IsTransit:    true,
				From:         stationDisplayName(leg.FromName(), leg.From()),
				To:           stationDisplayName(leg.ToName(), leg.To()),
				Duration:     fmt.Sprintf("%d min", durMinutes),
				DurationMins: durMinutes,
				TimeRange:    timeRange,
				Label:        labelStr,
			})
		}
	}
	return displays
}

func stationDisplayName(providerName string, id domain.StationID) string {
	name := strings.TrimSpace(providerName)
	if name == "" {
		return formatStationName(string(id))
	}
	if open := strings.LastIndex(name, " ("); open > 0 && strings.HasSuffix(name, ")") {
		if trimmed := strings.TrimSpace(name[:open]); trimmed != "" {
			name = trimmed
		}
	}
	return name
}

func formatStationName(id string) string {
	parts := strings.Split(id, "-")
	for i, part := range parts {
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func weekdays(input app.CommuteInput) map[string]bool {
	active := make(map[string]bool, len(input.Weekdays))
	for _, day := range input.Weekdays {
		switch day {
		case time.Monday:
			active["monday"] = true
		case time.Tuesday:
			active["tuesday"] = true
		case time.Wednesday:
			active["wednesday"] = true
		case time.Thursday:
			active["thursday"] = true
		case time.Friday:
			active["friday"] = true
		case time.Saturday:
			active["saturday"] = true
		case time.Sunday:
			active["sunday"] = true
		}
	}
	return active
}

func (s *Server) renderPage(writer http.ResponseWriter, request *http.Request, status int, data pageData) {
	if data.Commutes == nil {
		if commutes, err := s.deps.Commutes.List(request.Context(), ownerIDFromContext(request.Context())); err == nil {
			data.Commutes = commuteListItems(commutes)
		}
	}
	if data.CommuteID != "" && data.SavedCombinations == nil {
		if record, err := s.deps.Commutes.Get(request.Context(), ownerIDFromContext(request.Context()), data.CommuteID); err == nil {
			s.populateSavedCombinations(&data, record)
		}
	}
	s.render(writer, status, data)
}

func (s *Server) render(writer http.ResponseWriter, status int, data pageData) {
	var body bytes.Buffer
	if err := s.templates.ExecuteTemplate(&body, s.templateName, data); err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	if _, err := writer.Write(body.Bytes()); err != nil {
		return
	}
}

func validateExplorationParams(record app.CommuteRecord, dateStr, startStr, endStr string, now time.Time, loc *time.Location) (time.Time, []time.Time, string, bool) {
	var serviceDate time.Time
	var err error
	if dateStr != "" {
		serviceDate, err = time.ParseInLocation("2006-01-02", dateStr, loc)
		if err != nil {
			return time.Time{}, nil, "La date d'exploration est invalide.", false
		}
		if !record.Commute.Schedule().IsActive(serviceDate.Weekday()) {
			return time.Time{}, nil, "Le trajet n’est pas actif à cette date.", false
		}
	} else {
		serviceDate = record.Commute.Schedule().NextActiveDate(now, loc)
	}

	if (startStr != "" && endStr == "") || (startStr == "" && endStr != "") {
		return time.Time{}, nil, "Veuillez spécifier à la fois l'heure de début et de fin pour la plage personnalisée.", false
	}

	if startStr == "" && endStr == "" {
		return serviceDate, app.DefaultExplorationInstants(serviceDate, loc), "", true
	}

	start, err := domain.NewDepartureTime(startStr)
	if err != nil {
		return time.Time{}, nil, "L'heure de début de la plage est invalide.", false
	}
	end, err := domain.NewDepartureTime(endStr)
	if err != nil {
		return time.Time{}, nil, "L'heure de fin de la plage est invalide.", false
	}
	instants, err := app.ExplorationInstants(serviceDate, start, end, loc)
	if err != nil {
		return time.Time{}, nil, "La plage de recherche doit être croissante.", false
	}
	return serviceDate, instants, "", true
}

func patternDisplayLabel(pattern domain.RoutePattern) string {
	if display := strings.TrimSpace(pattern.Combination()); display != "" {
		return display
	}
	return "Transports en commun"
}

func buildPatternLegsDisplay(pattern domain.RoutePattern) []routeLegDisplay {
	var displays []routeLegDisplay
	for _, leg := range pattern.Legs() {
		var labelStr string
		label := leg.Label()
		var parts []string
		if label.Network() != "" {
			parts = append(parts, label.Network())
		}
		if label.Code() != "" {
			parts = append(parts, label.Code())
		}
		labelStr = strings.Join(parts, " ")

		displays = append(displays, routeLegDisplay{
			IsTransit: true,
			From:      stationDisplayName(leg.BoardName(), domain.StationID(leg.BoardAt())),
			To:        stationDisplayName(leg.AlightName(), domain.StationID(leg.AlightAt())),
			Label:     labelStr,
		})
	}
	return displays
}

func (s *Server) populateSavedCombinations(data *pageData, record app.CommuteRecord) {
	data.SavedCombinations = make([]savedCombinationDisplay, 0, len(record.SavedCombinations))
	for _, comb := range record.SavedCombinations {
		data.SavedCombinations = append(data.SavedCombinations, savedCombinationDisplay{
			ID:          string(comb.ID),
			Combination: comb.Pattern.Combination(),
			Legs:        buildPatternLegsDisplay(comb.Pattern),
		})
	}
}

func (s *Server) exploreCommuteRequest(writer http.ResponseWriter, request *http.Request) {
	commuteID := app.CommuteID(request.PathValue("commuteID"))
	record, err := s.deps.Commutes.Get(request.Context(), ownerIDFromContext(request.Context()), commuteID)
	if errors.Is(err, app.ErrCommuteNotFound) {
		s.notFound(writer, request)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, createFormMaxBytes)
	if err := parseForm(request); err != nil {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "La taille du formulaire dépasse la limite autorisée."
		s.renderPage(writer, request, http.StatusRequestEntityTooLarge, data)
		return
	}

	dateStr := request.PostForm.Get("date")
	startStr := request.PostForm.Get("window_start")
	endStr := request.PostForm.Get("window_end")

	serviceDate, instants, errMsg, ok := validateExplorationParams(record, dateStr, startStr, endStr, s.deps.Now(), s.location)
	if !ok {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = errMsg
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}

	discovery := app.NewRoutePatternDiscovery(s.deps.Routes)
	candidates, err := discovery.Discover(request.Context(), record.Commute, instants)
	if errors.Is(err, app.ErrRateLimited) {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "L’évaluation des itinéraires est limitée."
		s.renderPage(writer, request, http.StatusTooManyRequests, data)
		return
	}
	if errors.Is(err, app.ErrProviderUnavailable) || errors.Is(err, app.ErrProviderUnsupportedContract) {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, false, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "L'évaluation PRIM est indisponible dans cette version."
		s.renderPage(writer, request, http.StatusServiceUnavailable, data)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}

	data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
	s.populateSavedCombinations(&data, record)
	data.ExplorationDate = serviceDate.Format("2006-01-02")
	data.ExplorationStart = startStr
	data.ExplorationEnd = endStr
	data.HasExploredResult = true

	data.ExploredCandidates = make([]discoveredCandidateDisplay, 0, len(candidates))
	for _, cand := range candidates {
		data.ExploredCandidates = append(data.ExploredCandidates, discoveredCandidateDisplay{
			Fingerprint: cand.Pattern.Fingerprint(),
			Display:     patternDisplayLabel(cand.Pattern),
			Legs:        buildPatternLegsDisplay(cand.Pattern),
		})
	}

	s.renderPage(writer, request, http.StatusOK, data)
}

func (s *Server) saveCombinationsRequest(writer http.ResponseWriter, request *http.Request) {
	commuteID := app.CommuteID(request.PathValue("commuteID"))
	record, err := s.deps.Commutes.Get(request.Context(), ownerIDFromContext(request.Context()), commuteID)
	if errors.Is(err, app.ErrCommuteNotFound) {
		s.notFound(writer, request)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, createFormMaxBytes)
	if err := parseForm(request); err != nil {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "La taille du formulaire dépasse la limite autorisée."
		s.renderPage(writer, request, http.StatusRequestEntityTooLarge, data)
		return
	}

	selectedFingerprints := request.PostForm["fingerprints"]
	if len(selectedFingerprints) == 0 {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "Veuillez sélectionner au moins un parcours à enregistrer."
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}

	dateStr := request.PostForm.Get("date")
	startStr := request.PostForm.Get("window_start")
	endStr := request.PostForm.Get("window_end")

	_, instants, errMsg, ok := validateExplorationParams(record, dateStr, startStr, endStr, s.deps.Now(), s.location)
	if !ok {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = errMsg
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}

	discovery := app.NewRoutePatternDiscovery(s.deps.Routes)
	candidates, err := discovery.Discover(request.Context(), record.Commute, instants)
	if err != nil {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "Erreur lors de la vérification des parcours."
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}

	candidateMap := make(map[string]domain.RoutePattern)
	for _, candidate := range candidates {
		candidateMap[candidate.Pattern.Fingerprint()] = candidate.Pattern
	}

	var patternsToSave []domain.RoutePattern
	for _, fp := range selectedFingerprints {
		pattern, exists := candidateMap[fp]
		if !exists {
			data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
			s.populateSavedCombinations(&data, record)
			data.Error = "Certains parcours sélectionnés sont invalides ou obsolètes."
			s.renderPage(writer, request, http.StatusBadRequest, data)
			return
		}
		patternsToSave = append(patternsToSave, pattern)
	}

	_, err = s.deps.Commutes.ReplaceSavedRouteCombinations(request.Context(), ownerIDFromContext(request.Context()), commuteID, patternsToSave)
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}

	http.Redirect(writer, request, "/commutes/"+string(commuteID), http.StatusSeeOther)
}

func (s *Server) planDeparturesRequest(writer http.ResponseWriter, request *http.Request) {
	commuteID := app.CommuteID(request.PathValue("commuteID"))
	record, err := s.deps.Commutes.Get(request.Context(), ownerIDFromContext(request.Context()), commuteID)
	if errors.Is(err, app.ErrCommuteNotFound) {
		s.notFound(writer, request)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, createFormMaxBytes)
	if err := parseForm(request); err != nil {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "La taille du formulaire dépasse la limite autorisée."
		s.renderPage(writer, request, http.StatusRequestEntityTooLarge, data)
		return
	}

	selectedIDsRaw := request.PostForm["saved_combination_ids"]
	if len(selectedIDsRaw) == 0 {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "Veuillez sélectionner au moins un parcours à planifier."
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}

	selectedIDs := make([]app.SavedCombinationID, len(selectedIDsRaw))
	for i, id := range selectedIDsRaw {
		selectedIDs[i] = app.SavedCombinationID(id)
	}

	dateStr := request.PostForm.Get("date")
	startStr := request.PostForm.Get("window_start")
	endStr := request.PostForm.Get("window_end")

	serviceDate, _, errMsg, ok := validateExplorationParams(record, dateStr, startStr, endStr, s.deps.Now(), s.location)
	if !ok {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = errMsg
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}

	if s.deps.Planner == nil {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "Le planificateur de départ est indisponible."
		s.renderPage(writer, request, http.StatusServiceUnavailable, data)
		return
	}

	var windowStart time.Time
	var windowDuration time.Duration

	if startStr != "" && endStr != "" {
		startTime, _ := domain.NewDepartureTime(startStr)
		endTime, _ := domain.NewDepartureTime(endStr)
		windowStart = time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), startTime.Hour(), startTime.Minute(), 0, 0, s.location)
		windowEnd := time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), endTime.Hour(), endTime.Minute(), 0, 0, s.location)
		windowDuration = windowEnd.Sub(windowStart)
	} else {
		depTime := record.Commute.Schedule().DepartureTime()
		windowStart = time.Date(serviceDate.Year(), serviceDate.Month(), serviceDate.Day(), depTime.Hour(), depTime.Minute(), 0, 0, s.location)
		windowDuration = 30 * time.Minute
	}

	planner := app.NewSavedCombinationRuntimePlanner(s.deps.Planner)
	plans, err := planner.Plan(request.Context(), record, selectedIDs, windowStart, windowDuration)
	if errors.Is(err, app.ErrRateLimited) {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "L’évaluation des itinéraires est limitée."
		s.renderPage(writer, request, http.StatusTooManyRequests, data)
		return
	}
	if errors.Is(err, app.ErrSavedCombinationNotFound) {
		data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
		s.populateSavedCombinations(&data, record)
		data.Error = "Certains parcours sélectionnés sont invalides ou obsolètes."
		s.renderPage(writer, request, http.StatusBadRequest, data)
		return
	}
	if err != nil {
		http.Error(writer, "Erreur interne du serveur.", http.StatusInternalServerError)
		return
	}

	data := newPageData(app.InputFromCommuteRecord(record), record.ID, s.deps.ProviderAvailable, s.deps.Now())
	s.populateSavedCombinations(&data, record)
	data.PlanningDate = dateStr
	data.PlanningStart = startStr
	data.PlanningEnd = endStr
	data.HasPlannedResult = true

	data.PlannedResults = make([]plannedResultDisplay, 0, len(plans))
	firstViable := true
	for _, plan := range plans {
		var outcomeText string
		var outcomeType string
		switch plan.Outcome {
		case app.SavedCombinationViable:
			outcomeText = "Départs viables trouvés"
			outcomeType = "viable"
		case app.SavedCombinationNoViableDeparture:
			outcomeText = "Aucun départ viable trouvé sur cette plage."
			outcomeType = "no-departure"
		case app.SavedCombinationNoTimetableCoverage:
			outcomeText = "Pas de couverture horaire pour ce parcours."
			outcomeType = "no-timetable"
		case app.SavedCombinationProviderUnavailable:
			outcomeText = "Le fournisseur d'horaires est indisponible."
			outcomeType = "provider-unavailable"
		}

		isBest := false
		if plan.Outcome == app.SavedCombinationViable && firstViable {
			isBest = true
			firstViable = false
		}

		data.PlannedResults = append(data.PlannedResults, plannedResultDisplay{
			ID:          string(plan.Combination.ID),
			Combination: plan.Combination.Pattern.Combination(),
			Outcome:     outcomeText,
			OutcomeType: outcomeType,
			Options:     departureOptionDisplays(plan.Options),
			IsBest:      isBest,
		})
	}

	s.renderPage(writer, request, http.StatusOK, data)
}
