package tgbot

import "fmt"

var messages = map[string]map[string]string{
	"ru": {
		"accessDenied": "Доступ закрыт.\nВаш Telegram ID: %d\nПопросите администратора привязать его в настройках Polka.",
		"privateOnly":  "Бот работает только в личных сообщениях.",
		"start":        "Бот домашней библиотеки Polka.\n\nВыберите действие или отправьте /help.",
		"help": "Команды:\n" +
			"/search <запрос> — поиск по библиотеке\n" +
			"/title <название> — поиск по названию\n" +
			"/author <автор> — книги автора\n" +
			"/series <серия> — книги серии\n" +
			"/random — случайная книга\n" +
			"/recent — недавно добавленные\n" +
			"/id — ваша учётная запись\n" +
			"/language — язык бота\n" +
			"/cancel — отменить ввод\n\n" +
			"Администраторам также доступны /stats и /status.\n" +
			"Файлы книг бот не отправляет.",
		"searchPrompt":      "Отправьте запрос для поиска по библиотеке.",
		"titlePrompt":       "Отправьте название книги.",
		"authorPrompt":      "Отправьте имя автора.",
		"seriesPrompt":      "Отправьте название серии.",
		"cancelled":         "Ввод отменён.",
		"noResults":         "Ничего не найдено.",
		"searchTitle":       "Результаты поиска: %s",
		"titleTitle":        "Книги по названию: %s",
		"recentTitle":       "Недавно добавленные книги",
		"authorsTitle":      "Авторы по запросу: %s",
		"seriesTitle":       "Серии по запросу: %s",
		"booksAuthor":       "Книги автора: %s",
		"booksSeries":       "Книги серии: %s",
		"page":              "Страница %d",
		"hintRefine":        "Уточните запрос, если нужного автора или серии нет в списке.",
		"bookNotFound":      "Книга не найдена.",
		"authors":           "Авторы",
		"series":            "Серия",
		"bookID":            "ID книги",
		"genres":            "Жанры",
		"language":          "Язык",
		"format":            "Формат",
		"size":              "Размер",
		"rating":            "Рейтинг Polka",
		"noRating":          "Оценок пока нет",
		"ratingCount":       "оценок: %d",
		"external":          "Внешние оценки",
		"reading":           "Прочитано: %.0f%%",
		"finished":          "Прочитано полностью",
		"notStarted":        "Не начата",
		"annotation":        "Аннотация",
		"random":            "Случайная книга",
		"recent":            "Недавние книги",
		"stats":             "Статистика",
		"status":            "Состояние",
		"languageMenu":      "Выберите язык бота.",
		"languageSaved":     "Язык сохранён: %s.",
		"adminOnly":         "Команда доступна только администратору.",
		"idTitle":           "Учётная запись",
		"idLogin":           "Логин: %s",
		"idRole":            "Роль: %s",
		"idUserID":          "ID пользователя: %d",
		"idTelegram":        "Telegram ID: %d",
		"roleAdmin":         "админ",
		"roleUser":          "читатель",
		"menu":              "Меню",
		"home":              "В начало",
		"prev":              "Назад",
		"next":              "Дальше",
		"statsText":         "Книг: %d\nПользователей: %d\nВерсия: %s\nUptime: %s\nЗанято: %s\nСвободно: %s",
		"statsDiskNA":       "нет данных",
		"statusText":        "Импорт: %s\nОчередь конвертаций: %d в очереди, %d в работе, %d с ошибкой\nПоследние ошибки:\n%s",
		"statusIdle":        "простаивает",
		"statusRun":         "выполняется, этап %s, обработано %d",
		"statusDone":        "завершён, книг: %d",
		"statusErr":         "ошибка: %s",
		"noErrors":          "нет",
		"uploadOnlyAdmin":   "Загрузка доступна только администратору.",
		"uploadHint":        "Отправьте файл книги: FB2, EPUB, PDF, TXT, MOBI или AZW3 (до 20 МБ).",
		"uploadUnsupported": "Неподдерживаемый формат. Разрешены FB2, EPUB, PDF, TXT, MOBI и AZW3.",
		"uploadTooLarge":    "Файл больше 20 МБ — Telegram не позволяет боту скачать его.",
		"uploadEmpty":       "Файл пустой.",
		"uploadBadContent":  "Содержимое файла не похоже на выбранный формат.",
		"uploadDownload":    "Скачиваю файл…",
		"uploadAdded":       "Добавлено: %s",
		"uploadDuplicate":   "Дубликат: %s",
		"uploadQueued":      "Поставлено в очередь конвертации: %s",
		"uploadError":       "Ошибка: %s",
		"uploadLimit":       "Лимит загрузок: %d в час. Попробуйте позже.",
		"uploadLimitWait":   "Лимит загрузок исчерпан. Следующая попытка через %s.",
		"unknown":           "Не понял команду. Отправьте /help.",
		"btnSearch":         "Поиск",
		"btnRandom":         "Случайная",
		"btnRecent":         "Недавние",
		"btnStats":          "Статистика",
		"btnStatus":         "Состояние",
		"btnLanguage":       "Язык",
		"btnHelp":           "Помощь",
	},
	"en": {
		"accessDenied": "Access denied.\nYour Telegram ID: %d\nAsk an administrator to link it in Polka settings.",
		"privateOnly":  "This bot works in private chats only.",
		"start":        "Polka home library bot.\n\nChoose an action or send /help.",
		"help": "Commands:\n" +
			"/search <query> — search the library\n" +
			"/title <title> — search by title\n" +
			"/author <author> — books by author\n" +
			"/series <series> — books in a series\n" +
			"/random — random book\n" +
			"/recent — recently added\n" +
			"/id — your account\n" +
			"/language — bot language\n" +
			"/cancel — cancel input\n\n" +
			"Administrators also have /stats and /status.\n" +
			"The bot never sends book files.",
		"searchPrompt":      "Send a query to search the library.",
		"titlePrompt":       "Send a book title.",
		"authorPrompt":      "Send an author name.",
		"seriesPrompt":      "Send a series title.",
		"cancelled":         "Input cancelled.",
		"noResults":         "Nothing found.",
		"searchTitle":       "Search results: %s",
		"titleTitle":        "Books by title: %s",
		"recentTitle":       "Recently added books",
		"authorsTitle":      "Authors matching: %s",
		"seriesTitle":       "Series matching: %s",
		"booksAuthor":       "Books by: %s",
		"booksSeries":       "Books in series: %s",
		"page":              "Page %d",
		"hintRefine":        "Refine the query if the author or series is not in the list.",
		"bookNotFound":      "Book not found.",
		"authors":           "Authors",
		"series":            "Series",
		"bookID":            "Book ID",
		"genres":            "Genres",
		"language":          "Language",
		"format":            "Format",
		"size":              "Size",
		"rating":            "Polka rating",
		"noRating":          "No ratings yet",
		"ratingCount":       "ratings: %d",
		"external":          "External ratings",
		"reading":           "Read: %.0f%%",
		"finished":          "Finished",
		"notStarted":        "Not started",
		"annotation":        "Annotation",
		"random":            "Random book",
		"recent":            "Recent books",
		"stats":             "Statistics",
		"status":            "Status",
		"languageMenu":      "Choose the bot language.",
		"languageSaved":     "Language saved: %s.",
		"adminOnly":         "This command is available to administrators only.",
		"idTitle":           "Account",
		"idLogin":           "Login: %s",
		"idRole":            "Role: %s",
		"idUserID":          "User ID: %d",
		"idTelegram":        "Telegram ID: %d",
		"roleAdmin":         "administrator",
		"roleUser":          "reader",
		"menu":              "Menu",
		"home":              "Home",
		"prev":              "Back",
		"next":              "Next",
		"statsText":         "Books: %d\nUsers: %d\nVersion: %s\nUptime: %s\nUsed: %s\nFree: %s",
		"statsDiskNA":       "unavailable",
		"statusText":        "Import: %s\nConversion queue: %d queued, %d processing, %d failed\nRecent errors:\n%s",
		"statusIdle":        "idle",
		"statusRun":         "running, phase %s, processed %d",
		"statusDone":        "done, books: %d",
		"statusErr":         "error: %s",
		"noErrors":          "none",
		"uploadOnlyAdmin":   "Uploads are available to administrators only.",
		"uploadHint":        "Send a book file: FB2, EPUB, PDF, TXT, MOBI or AZW3 (up to 20 MB).",
		"uploadUnsupported": "Unsupported format. Allowed: FB2, EPUB, PDF, TXT, MOBI and AZW3.",
		"uploadTooLarge":    "The file is larger than 20 MB, so Telegram will not let the bot download it.",
		"uploadEmpty":       "The file is empty.",
		"uploadBadContent":  "The file content does not look like the declared format.",
		"uploadDownload":    "Downloading the file…",
		"uploadAdded":       "Added: %s",
		"uploadDuplicate":   "Duplicate: %s",
		"uploadQueued":      "Queued for conversion: %s",
		"uploadError":       "Error: %s",
		"uploadLimit":       "Upload limit: %d per hour. Try again later.",
		"uploadLimitWait":   "Upload limit reached. Next attempt in %s.",
		"unknown":           "Unknown command. Send /help.",
		"btnSearch":         "Search",
		"btnRandom":         "Random",
		"btnRecent":         "Recent",
		"btnStats":          "Statistics",
		"btnStatus":         "Status",
		"btnLanguage":       "Language",
		"btnHelp":           "Help",
	},
}

func botCommands(lang string) []BotCommand {
	if lang == "ru" {
		return []BotCommand{
			{"start", "Начать"},
			{"search", "Поиск по библиотеке"},
			{"title", "Поиск по названию"},
			{"author", "Книги автора"},
			{"series", "Книги серии"},
			{"random", "Случайная книга"},
			{"recent", "Недавние книги"},
			{"id", "Моя учётная запись"},
			{"language", "Язык бота"},
			{"stats", "Статистика (админ)"},
			{"status", "Состояние (админ)"},
			{"cancel", "Отменить ввод"},
			{"help", "Помощь"},
		}
	}
	return []BotCommand{
		{"start", "Start"},
		{"search", "Search the library"},
		{"title", "Search by title"},
		{"author", "Books by author"},
		{"series", "Books in a series"},
		{"random", "Random book"},
		{"recent", "Recently added"},
		{"id", "My account"},
		{"language", "Bot language"},
		{"stats", "Statistics (admin)"},
		{"status", "Status (admin)"},
		{"cancel", "Cancel input"},
		{"help", "Help"},
	}
}

func (b *Bot) tr(lang, key string, args ...any) string {
	table, ok := messages[lang]
	if !ok {
		table = messages["ru"]
	}
	s, ok := table[key]
	if !ok {
		s = key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
