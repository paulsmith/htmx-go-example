package main

import (
	"bytes"
	embed "embed"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/gorilla/csrf"
)

//go:embed template
var f embed.FS

func logger(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.Method, r.URL, r.RemoteAddr)
		h.ServeHTTP(w, r)
	})
}

var latestTodoId uint64

type todo struct {
	Id        uint64
	Text      string
	CreatedAt time.Time
	Done      bool
	DoneAt    time.Time
	Deleted   bool
	DeletedAt time.Time
}

type todoService interface {
	getTodoById(id uint64) (*todo, error)
	findTodos(filter todoFilter) ([]*todo, error)
	createTodo(todo *todo) error
	updateTodo(id uint64, update todoUpdate) (*todo, error)
	deleteTodo(id uint64) error
	deleteTodos(ids []uint64) error
}

type todoFilter struct {
	done *bool
}

type todoUpdate struct {
	text *string
	done *bool
}

type inMemTodoService struct {
	todos []*todo
}

func (s *inMemTodoService) getTodoById(id uint64) (*todo, error) {
	for i := range s.todos {
		if s.todos[i].Id == id {
			return s.todos[i], nil
		}
	}
	return nil, fmt.Errorf("todo %d not found", id)
}

func (s *inMemTodoService) findTodos(filter todoFilter) ([]*todo, error) {
	var todos []*todo
	for _, t := range s.todos {
		if t.Deleted {
			continue
		}
		if filter.done != nil {
			if t.Done == *filter.done {
				todos = append(todos, t)
			}
		} else {
			todos = append(todos, t)
		}
	}
	return todos, nil
}

func (s *inMemTodoService) createTodo(todo *todo) error {
	todo.Text = strings.TrimSpace(todo.Text)
	if todo.Text == "" {
		return fmt.Errorf("todo text is required")
	}
	todo.Id = atomic.AddUint64(&latestTodoId, 1)
	todo.Done = false
	todo.CreatedAt = time.Now()
	todo.DoneAt = time.Time{}
	todo.Deleted = false
	todo.DeletedAt = time.Time{}
	s.todos = append(s.todos, todo)
	return nil
}

func (s *inMemTodoService) updateTodo(id uint64, update todoUpdate) (*todo, error) {
	for i, t := range s.todos {
		if t.Id == id {
			if update.text != nil {
				s.todos[i].Text = *update.text
			}
			if update.done != nil {
				s.todos[i].Done = *update.done
				if *update.done {
					s.todos[i].DoneAt = time.Now()
				}
			}
			return s.todos[i], nil
		}
	}
	return nil, fmt.Errorf("todo %d not found", id)
}

func (s *inMemTodoService) deleteTodo(id uint64) error {
	for i, t := range s.todos {
		if t.Id == id {
			s.todos[i].Deleted = true
			s.todos[i].DeletedAt = time.Now()
			return nil
		}
	}
	return fmt.Errorf("todo %d not found", id)
}

func (s *inMemTodoService) deleteTodos(ids []uint64) error {
	var deleted int
	for i, t := range s.todos {
		for _, id := range ids {
			if t.Id == id {
				s.todos[i].Deleted = true
				s.todos[i].DeletedAt = time.Now()
			}
			deleted++
		}
	}
	if deleted != len(ids) {
		return fmt.Errorf("could not delete all todos (%d of %d)", deleted, len(ids))
	}
	return nil
}

type server struct {
	templates   map[string]*template.Template
	todoService todoService
}

func debugLog(fmt string, a ...interface{}) {
	if _, ok := os.LookupEnv("DEBUG"); ok {
		log.Printf("\x1b[1;32m[DEBUG]\x1b[0m "+fmt, a...)
	}
}

func preprocessTemplates(basePath string, partialPaths, pagePaths []string, funcs template.FuncMap) map[string]*template.Template {
	templates := make(map[string]*template.Template)

	filename := filepath.Base(basePath)
	base := template.New(filename).Funcs(funcs)
	debugLog("parsing base %s", basePath)
	base = template.Must(base.ParseFS(f, basePath))
	templates[base.Name()] = base

	for _, path := range partialPaths {
		debugLog("parsing partial %s", path)
		t := template.Must(base.ParseFS(f, path))
		filename := filepath.Base(path)
		templates[filename] = t
	}

	for _, path := range pagePaths {
		debugLog("parsing page %s", path)
		base := template.Must(templates[base.Name()].Clone())
		t := template.Must(base.ParseFS(f, path))
		filename := filepath.Base(path)
		templates[filename] = t
	}

	return templates
}

func newServer() *server {
	s := &server{}

	funcs := template.FuncMap{
		"activeLang": func(r *http.Request) language.Tag {
			return r.Context().Value(languageTagKey).(language.Tag)
		},

		"languages": func() []Language {
			return supportedLanguages
		},

		"T": func(r *http.Request, key string, a ...interface{}) string {
			p := r.Context().Value(messagePrinterKey).(*message.Printer)
			return p.Sprintf(key, a...)
		},

		"csrfToken": func(r *http.Request) string {
			return csrf.Token(r)
		},
	}

	s.templates = setupTemplates(funcs)
	s.todoService = &inMemTodoService{}

	return s
}

func setupTemplates(funcs template.FuncMap) map[string]*template.Template {
	mustGlob := func(matches []string, err error) []string {
		if err != nil {
			panic(err)
		}
		return matches
	}

	basePath := filepath.Join("template", "base.html")
	partialPaths := mustGlob(fs.Glob(f, filepath.Join("template", "partial", "*.html")))
	pagePaths := mustGlob(fs.Glob(f, filepath.Join("template", "page", "*.html")))

	return preprocessTemplates(basePath, partialPaths, pagePaths, funcs)
}

func renderPage(templates map[string]*template.Template, name string, w http.ResponseWriter, data interface{}) error {
	w.Header().Set("Content-Type", "text/html")
	t, ok := templates[name]
	if !ok {
		return fmt.Errorf("unknown template %q", name)
	}
	var b bytes.Buffer
	var err error
	if err = t.ExecuteTemplate(&b, name, data); err != nil {
		return fmt.Errorf("executing template %q: %w", name, err)
	}
	if _, err = io.Copy(w, &b); err != nil {
		return fmt.Errorf("copying rendered template to response: %w", err)
	}
	return nil
}

func handlePage(templates map[string]*template.Template, name string, w http.ResponseWriter, data interface{}) error {
	if err := renderPage(templates, name, w, data); err != nil {
		log.Printf("rendering page: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return err
	}
	return nil
}

func (s *server) indexHandler(w http.ResponseWriter, r *http.Request) {
	handlePage(s.templates, "index.html", w, struct {
		Request *http.Request
	}{
		r,
	})
}

type paramFilter struct {
	Label  string
	Value  string
	Active bool
}

func getParamFilters() []paramFilter {
	paramFilters := []paramFilter{
		{Label: "All", Value: "", Active: true},
		{Label: "Remaining", Value: "notdone"},
		{Label: "Done", Value: "done"},
	}
	return paramFilters
}

func applyFilter(filter *todoFilter, filters []paramFilter, r *http.Request) {
	if v := r.FormValue("filter"); v != "" {
		for i, f := range filters {
			if f.Value == v {
				filters[i].Active = true
			} else {
				filters[i].Active = false
			}
		}

		var done bool
		switch v {
		case "done":
			done = true
			filter.done = &done
		case "notdone":
			done = false
			filter.done = &done
		default:
			log.Printf("[WARN] unknown filter value %q", v)
		}
	}
}

func (s *server) getFilteredTodoListItems(r *http.Request, updateNumber bool) ([]todoListItem, []paramFilter, error) {
	paramFilters := getParamFilters()
	var filter todoFilter
	applyFilter(&filter, paramFilters, r)
	todos, err := s.todoService.findTodos(filter)
	if err != nil {
		return nil, nil, fmt.Errorf("finding todos: %w", err)
	}
	items := make([]todoListItem, len(todos))
	for i, t := range todos {
		items[i] = todoListItem{
			Request:             r,
			Todo:                t,
			UpdateNumber:        updateNumber,
			FilteredTodosNumber: len(todos),
		}
	}
	return items, paramFilters, nil
}

type todoListItem struct {
	Request             *http.Request
	Todo                *todo
	UpdateNumber        bool
	FilteredTodosNumber int
}

func (s *server) todosIndexHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		newTodo := r.FormValue("new-todo")
		newTodo = strings.TrimSpace(newTodo)
		if newTodo == "" {
			log.Printf("invalid todo form")
			// invalid form, render page with errors
		} else {
			todo := todo{Text: newTodo}
			err := s.todoService.createTodo(&todo)
			if err != nil {
				log.Printf("creating todo: %v", err)
				http.Error(w, http.StatusText(500), 500)
				return
			}
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Trigger", "newTodo")
				handlePage(s.templates, "new-todo-form.html", w, map[string]interface{}{
					"Request": r,
				})
			} else {
				http.Redirect(w, r, "/todos/", 302)
			}
			return
		}
	}

	todos, paramFilters, err := s.getFilteredTodoListItems(r, false)
	if err != nil {
		log.Printf("finding todos: %v", err)
		http.Error(w, http.StatusText(500), 500)
		return
	}

	data := struct {
		Request             *http.Request
		Todos               []todoListItem
		UpdateNumber        bool
		FilteredTodosNumber int
		Filters             []paramFilter
		Errors              []string
		CSRFTemplateTag     template.HTML
	}{
		r,
		todos,
		false,
		len(todos),
		paramFilters,
		nil,
		csrf.TemplateField(r),
	}

	if r.Header.Get("HX-Request") == "true" {
		handlePage(s.templates, "todo-list.html", w, data)
	} else {
		handlePage(s.templates, "todos_index.html", w, data)
	}
}

func isTodoInList(todo *todo, list []todoListItem) bool {
	for _, item := range list {
		if todo == item.Todo {
			return true
		}
	}
	return false
}

func (s *server) todoHandler(w http.ResponseWriter, r *http.Request) {
	id, err := extractTodoId(r.URL.Path)
	if err != nil {
		log.Printf("extracting todo id: %v", err)
		http.Error(w, http.StatusText(500), 500)
		return
	}
	if r.Method == "GET" {
		todo, err := s.todoService.getTodoById(id)
		if err != nil {
			log.Printf("getting todo by id: %v", err)
			http.Error(w, http.StatusText(500), 500)
			return
		}
		data := todoListItem{
			Request:      r,
			Todo:         todo,
			UpdateNumber: false,
		}
		handlePage(s.templates, "todo-list-item.html", w, data)
	} else if r.Method == "DELETE" {
		if err := s.todoService.deleteTodo(id); err != nil {
			log.Printf("getting todo by id: %v", err)
			http.Error(w, http.StatusText(500), 500)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			todos, _, err := s.getFilteredTodoListItems(r, true)
			if err != nil {
				log.Printf("finding todos: %v", err)
				http.Error(w, http.StatusText(500), 500)
				return
			}
			data := todoListItem{
				Request:             r,
				Todo:                nil,
				UpdateNumber:        true,
				FilteredTodosNumber: len(todos),
			}
			handlePage(s.templates, "todo-list-number.html", w, data)
		}
	} else if r.Method == "PUT" {
		update := todoUpdate{}
		if strings.HasSuffix(r.URL.Path, "_done/") {
			done := r.FormValue("done") == "done"
			update.done = &done
		} else if strings.HasSuffix(r.URL.Path, "_text/") {
			text := r.FormValue("text")
			update.text = &text
		}
		todo, err := s.todoService.updateTodo(id, update)
		if err != nil {
			log.Printf("getting todo by id: %v", err)
			http.Error(w, http.StatusText(500), 500)
			return
		}
		todos, _, err := s.getFilteredTodoListItems(r, true)
		if err != nil {
			log.Printf("finding todos: %v", err)
			http.Error(w, http.StatusText(500), 500)
			return
		}
		if isTodoInList(todo, todos) {
			data := todoListItem{
				Request:             r,
				Todo:                todo,
				UpdateNumber:        true,
				FilteredTodosNumber: len(todos),
			}
			handlePage(s.templates, "todo-list-item.html", w, data)
		} else {
			data := todoListItem{
				Request:             r,
				Todo:                nil,
				UpdateNumber:        true,
				FilteredTodosNumber: len(todos),
			}
			handlePage(s.templates, "todo-list-number.html", w, data)
		}
	} else {
		http.Error(w, http.StatusText(405), 405)
		return
	}
}

func extractTodoId(path string) (uint64, error) {
	pat := regexp.MustCompile(`^/todos/(\d+)/`)
	matches := pat.FindStringSubmatch(path)
	id, err := strconv.ParseUint(matches[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing id string: %w", err)
	}
	return id, nil
}

func (s *server) todoEditHandler(w http.ResponseWriter, r *http.Request) {
	id, err := extractTodoId(r.URL.Path)
	if err != nil {
		log.Printf("extracting todo id: %v", err)
		http.Error(w, http.StatusText(500), 500)
		return
	}
	todo, err := s.todoService.getTodoById(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	handlePage(s.templates, "todo-edit-item.html", w, todo)
}

func (s *server) languageHandler(w http.ResponseWriter, r *http.Request) {
	if tag := r.FormValue("lang"); tag != "" {
		var isSupported bool
		for _, l := range supportedLanguages {
			if l.Tag == tag {
				isSupported = true
				break
			}
		}
		if !isSupported {
			log.Printf("[WARN] unsupported language tag %q", tag)
			http.NotFound(w, r)
			return
		}

		c := http.Cookie{
			Name:     langCookieName,
			Value:    tag,
			Path:     "/",
			SameSite: http.SameSiteLaxMode,
			HttpOnly: true,
		}
		http.SetCookie(w, &c)
		w.Header().Set("HX-Refresh", "true")
		return
	} else {
		http.Error(w, http.StatusText(400), 400)
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		s.indexHandler(w, r)
	} else if r.URL.Path == "/lang/" {
		s.languageHandler(w, r)
	} else if strings.HasPrefix(r.URL.Path, "/todos") {
		path := strings.TrimPrefix(r.URL.Path, "/todos")
		if path == "" {
			http.Redirect(w, r, "/todos/", 301)
		} else if path == "/" {
			s.todosIndexHandler(w, r)
		} else if matched, err := regexp.MatchString(`^/\d+/((_done|_text)/)?$`, path); err == nil && matched {
			s.todoHandler(w, r)
		} else if matched, err := regexp.MatchString(`^/\d+/edit/$`, path); err == nil && matched {
			s.todoEditHandler(w, r)
		} else {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		}
	} else {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	}
}

func main() {
	host := flag.String("host", "0.0.0.0", "hostname or IP address")
	port := flag.Int("port", 8080, "port")
	csrfAuthKey := flag.String("csrf", "", "CSRF auth key (32 bytes)")
	flag.Parse()

	if *csrfAuthKey == "" {
		if key := os.Getenv("CSRF_AUTH_KEY"); key != "" {
			*csrfAuthKey = key
		}
	}
	if *csrfAuthKey == "" || len(*csrfAuthKey) != 32 {
		log.Fatalf("CSRF auth key (32 bytes) required, please provide -csrf option or set CSRF_AUTH_KEY env var")
	}

	s := newServer()
	examples := []string{"Do some stuff", "Make other things", "Call your mom"}
	for _, ex := range examples {
		todo := todo{Text: ex}
		if err := s.todoService.createTodo(&todo); err != nil {
			panic(err)
		}
	}

	_, isDev := os.LookupEnv("DEV")
	log.Printf("\x1b[1;32mis development environment?\x1b[0m %v", isDev)

	var h http.Handler
	h = s
	h = csrf.Protect([]byte(*csrfAuthKey),
		csrf.Secure(!isDev),
		csrf.Path("/"),
	)(h)
	h = logger(h)
	h = withMessagePrinter(h)
	http.Handle("/", h)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
