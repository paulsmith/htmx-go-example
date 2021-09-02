package main

import (
	"context"
	"log"
	"net/http"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/message/catalog"
)

type Language struct {
	Tag        string
	Label      string
	WorldEmoji string
}

var supportedLanguages = []Language{
	{"en", "English", "🌎"},
	{"fr", "Français", "🌍"},
}

type entry struct {
	tag, key string
	msg      interface{}
}

var entries = [...]entry{
	{"en", "site-wide navigation", "site-wide navigation"},
	{"fr", "site-wide navigation", "navigation sur l'ensemble du site"},
	{"en", "navigation links", "navigation links"},
	{"fr", "navigation links", "liens de navigation"},
	{"en", "Todos", "Todos"},
	{"fr", "Todos", "À faire"},
	{"en", "main header", "main header"},
	{"fr", "main header", "en-tête principal"},
	{"en", "main page content", "main page content"},
	{"fr", "main page content", "contenu de la page principale"},
	{"en", "footer", "footer"},
	{"fr", "footer", "bas de page"},
	{"fr", "new todo form", "nouveau formulaire à faire"},
	{"fr", "new todo entry", "nouvelle entrée à faire"},
	{"fr", "list of todos", "liste de tâches"},
	{"fr", "Filter todos:", "Filtrer les tâches:"},
	{"en", "Select language", "Select language"},
	{"fr", "Select language", "Choisir la langue"},
	{"en", "Todo list", "Todo list"},
	{"fr", "Todo list", "Liste de choses à faire"},
	{"en", "Todo", "Todo"},
	{"fr", "Todo", "À faire"},
	{"en", "Done?", "Done?"},
	{"fr", "Done?", "Complété?"},
	{"en", "Actions", "Actions"},
	{"fr", "Actions", "Actions"},
	{"fr", "New todo", "Nouvelle tâche"},
	{"fr", "Show:", "Montrer:"},
	{"fr", "All", "Tout"},
	{"fr", "Done", "Complété"},
	{"fr", "Remaining", "Restant"},
	{"fr", "Mark done", "Marquer complété"},
	{"fr", "Mark undone", "Marquer inachevé"},
	{"fr", "Delete", "Supprimer"},
	{"en", "Showing %d todo item(s).", plural.Selectf(1, "",
		"=1", "Showing 1 todo item.",
		"=2", "Showing 2 todo items.",
		"other", "Showing %d todo items.",
	)},
	{"fr", "Showing %d todo item(s).", plural.Selectf(1, "",
		"=1", "Affichage de 1 élément à faire.",
		"=2", "Affichage de 2 éléments à faire.",
		"other", "Affichage de %d éléments à faire.",
	)},
	{"en", "intro(part)1", `This simple todo app demonstrates the effective use of `},
	{"en", "intro(part)2", `a way to enhance interactivity and responsiveness to basic HTML, with Go's html/template package.`},
	{"fr", "intro(part)1", "Cette application simple à faire montre l'utilisation efficace de "},
	{"fr", "intro(part)2", "un moyen d'améliorer l'interactivité et la réactivité au HTML de base, avec le package html/template de Go."},
	{"fr", "What to do …", "Que faire …"},
	{"fr", "Add", "Ajouter"},
	{"fr", "Copyright", "Droits d'auteur"},
	{"fr", "Are you sure?", "Es-tu sûr?"},
}

func init() {
	for _, e := range entries {
		tag := language.MustParse(e.tag)
		switch msg := e.msg.(type) {
		case string:
			if err := message.SetString(tag, e.key, msg); err != nil {
				panic(err)
			}
		case catalog.Message:
			message.Set(tag, e.key, msg)
		case []catalog.Message:
			message.Set(tag, e.key, msg...)
		}
	}
}

var matcher = language.NewMatcher([]language.Tag{
	language.English,
	language.French,
})

type contextKey int

const (
	messagePrinterKey contextKey = 1
	languageTagKey    contextKey = 2
	langCookieName               = "lang"
)

func withMessagePrinter(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang, err := r.Cookie(langCookieName)
		if err == http.ErrNoCookie {
			lang = &http.Cookie{Name: langCookieName, Value: ""}
		}
		accept := r.Header.Get("Accept-Language")
		log.Printf("\x1b[1;35mcookie: %q\taccept: %q\x1b[0m", lang, accept)
		tag, _ := language.MatchStrings(matcher, lang.Value, accept)
		log.Printf("\x1b[1;36muser language: %s\x1b[0m", tag)
		p := message.NewPrinter(tag)
		ctx := context.WithValue(context.Background(), messagePrinterKey, p)
		ctx = context.WithValue(ctx, languageTagKey, tag)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
