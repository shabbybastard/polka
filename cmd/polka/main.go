// Polka is a standalone home e-book library server:
// web interface, OPDS catalog and collection management.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shabbybastard/polka/internal/auth"
	"github.com/shabbybastard/polka/internal/collections"
	"github.com/shabbybastard/polka/internal/config"
	"github.com/shabbybastard/polka/internal/importer"
	"github.com/shabbybastard/polka/internal/library"
	"github.com/shabbybastard/polka/internal/server"
	"github.com/shabbybastard/polka/internal/store"
	"github.com/shabbybastard/polka/internal/tgbot"
	"github.com/shabbybastard/polka/web"
)

var version = "dev" // overridden at build time via -ldflags

const usage = `polka — сервер домашней библиотеки

Использование:
  polka [serve] [флаги]          запустить сервер (по умолчанию)
  polka import --inpx <файл>     импортировать inpx-каталог
      --replace                  заменить существующую коллекцию
      (путь можно указать и позиционным аргументом)
  polka passwd <логин> <пароль>  сменить пароль пользователя (восстановление доступа)
  polka collections import <файл.json>…  добавить/обновить подборки книг
  polka collections match [slug]         пересчитать сопоставление с библиотекой
  polka collections list                 показать подборки
  polka collections remove <slug>        удалить подборку
  polka version                  показать версию

Флаги: --addr, --data-dir, --library-dir, --telegram-token
        (или POLKA_ADDR, POLKA_DATA_DIR, POLKA_LIBRARY_DIR, POLKA_TELEGRAM_TOKEN)
`

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe(log, args)
	case "import":
		err = runImport(log, args)
	case "passwd":
		err = runPasswd(args)
	case "collections":
		err = runCollections(log, args)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error(cmd, "error", err)
		os.Exit(1)
	}
}

func runServe(log *slog.Logger, args []string) error {
	cfg, _, err := config.Load(args, nil)
	if err != nil {
		return err
	}
	if cfg.Auth == "desktop" {
		return errors.New("--auth=desktop is reserved for polka-desktop; use required, public or demo")
	}
	cfg.Version = version

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	webFS, err := web.Dist()
	if err != nil {
		return fmt.Errorf("embedded frontend: %w", err)
	}

	lib := library.New(cfg.LibraryDir)
	switch {
	case lib == nil:
		log.Warn("library-dir is not set: reading, covers and downloads are disabled",
			"hint", "set POLKA_LIBRARY_DIR or --library-dir")
	default:
		if h := lib.Health(); !h.Exists {
			log.Warn("library directory does not exist", "path", h.Root,
				"hint", "mount your books there, or set POLKA_LIBRARY_DIR to where they are")
		} else if h.Entries == 0 {
			log.Warn("library directory is empty", "path", h.Root,
				"hint", "book archives must live under this path; if you mounted them elsewhere, set POLKA_LIBRARY_DIR to that path")
		} else {
			log.Info("library directory", "path", h.Root, "entries", h.Entries)
		}
	}

	users, err := auth.Open(filepath.Join(cfg.DataDir, "users.db"))
	if err != nil {
		return fmt.Errorf("open users database: %w", err)
	}
	defer users.Close()

	// First run: create an administrator with a random password.
	ctx := context.Background()
	if n, err := users.UsersCount(ctx); err != nil {
		return err
	} else if n == 0 {
		password := auth.RandomPassword()
		if _, err := users.CreateUser(ctx, "admin", password, "Администратор", auth.RoleAdmin); err != nil {
			return err
		}
		log.Warn("created initial administrator — save this password, it is shown only once",
			"login", "admin", "password", password)
	}

	if err := server.PrepareCatalogRemap(ctx, st, users); err != nil {
		return fmt.Errorf("catalog remap: %w", err)
	}
	srv, app := server.NewWithApp(cfg, log, st, lib, users, nil, webFS)

	runCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	// The optional Telegram bot lives in its own package and is started only
	// in server mode with a configured token. Without it this block is a no-op.
	var botCancel context.CancelFunc
	var botDone chan struct{}
	if token := strings.TrimSpace(cfg.TelegramToken); token != "" {
		botCtx, cancel := context.WithCancel(runCtx)
		botCancel = cancel
		botDone = make(chan struct{})
		backend := app.TelegramBackend()
		go func() {
			defer close(botDone)
			if err := tgbot.Run(botCtx, token, backend, log); err != nil {
				log.Error("telegram bot", "error", err)
			}
		}()
		log.Info("telegram bot enabled")
	}
	stopBot := func() {
		if botCancel != nil {
			botCancel()
			<-botDone
		}
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("polka starting", "version", version, "addr", cfg.Addr, "db", cfg.DBPath())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		stopBot()
		app.Close()
		return err
	case <-runCtx.Done():
	}
	stopBot()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		app.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	app.Close()
	log.Info("polka stopped")
	return nil
}

// runPasswd is an emergency password change from the server console (when the
// administrator password is lost). Creates the user if missing.
func runPasswd(args []string) error {
	cfg, rest, err := config.Load(args, nil)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return errors.New("использование: polka passwd <логин> <новый-пароль>")
	}
	login, password := rest[0], rest[1]

	users, err := auth.Open(filepath.Join(cfg.DataDir, "users.db"))
	if err != nil {
		return err
	}
	defer users.Close()

	ctx := context.Background()
	list, err := users.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range list {
		if strings.EqualFold(u.Login, login) {
			if err := users.UpdateUser(ctx, u.ID, &password, nil, nil, nil); err != nil {
				return err
			}
			fmt.Printf("пароль пользователя %s обновлён\n", u.Login)
			return nil
		}
	}
	if _, err := users.CreateUser(ctx, login, password, "", auth.RoleAdmin); err != nil {
		return err
	}
	fmt.Printf("создан администратор %s\n", login)
	return nil
}

func runImport(log *slog.Logger, args []string) error {
	var replace bool
	var inpxFlag string
	cfg, rest, err := config.Load(args, func(fs *flag.FlagSet) {
		fs.BoolVar(&replace, "replace", false, "replace existing collection")
		fs.StringVar(&inpxFlag, "inpx", "", "path to the .inpx catalog file")
	})
	if err != nil {
		return err
	}
	inpxPath := inpxFlag
	if inpxPath == "" && len(rest) == 1 {
		inpxPath = rest[0] // also accept the path as a positional argument
	}
	if inpxPath == "" {
		return errors.New("укажите inpx-файл: polka import --inpx <файл.inpx>")
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	ctx := context.Background()
	if n, err := st.BookCount(ctx); err != nil {
		return err
	} else if n > 0 && !replace {
		return fmt.Errorf("library is not empty (%d books); use --replace", n)
	}

	var usersSvc *auth.Service
	usersPath := filepath.Join(cfg.DataDir, "users.db")
	if _, err := os.Stat(usersPath); err == nil {
		usersSvc, err = auth.Open(usersPath)
		if err != nil {
			return fmt.Errorf("open users database: %w", err)
		}
		defer usersSvc.Close()
	}
	hooks := importer.ReplaceHooks{
		BeforeReplace: func(oldKeys, newKeys map[int64]string) error {
			if usersSvc == nil {
				return nil
			}
			generation, err := st.CatalogGeneration(ctx)
			if err != nil {
				return err
			}
			return usersSvc.SavePendingRemap(ctx, generation+1, store.MapBookIDs(oldKeys, newKeys))
		},
		AfterReplace: func(oldKeys, newKeys map[int64]string) error {
			if usersSvc != nil {
				generation, err := st.CatalogGeneration(ctx)
				if err != nil {
					return err
				}
				if err := usersSvc.ApplyPendingRemap(ctx, generation); err != nil {
					return fmt.Errorf("remap user data (will retry on next start): %w", err)
				}
			}
			colsPath := filepath.Join(cfg.DataDir, "collections.db")
			if _, err := os.Stat(colsPath); err == nil {
				cols, err := collections.Open(colsPath)
				if err != nil {
					return fmt.Errorf("open collections database: %w", err)
				}
				resetErr := cols.ResetMatches(ctx)
				if resetErr == nil {
					if _, err := cols.MatchAll(ctx, st); err != nil {
						log.Warn("collections match", "error", err)
					}
				}
				cols.Close()
				if resetErr != nil {
					return fmt.Errorf("reset collection matches: %w", resetErr)
				}
			}
			return nil
		},
	}
	stats, err := importer.ReplaceInpx(ctx, log, st, inpxPath, cfg.DataDir, nil, hooks)
	if err != nil {
		return err
	}
	log.Info("import done",
		"books", stats.Books, "authors", stats.Authors, "series", stats.Series,
		"genres", stats.Genres, "duration", stats.Duration.Round(time.Millisecond))
	return nil
}
