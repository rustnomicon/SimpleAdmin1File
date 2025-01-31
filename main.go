package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "github.com/ClickHouse/clickhouse-go" // ClickHouse
	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql" // MySQL
	_ "github.com/lib/pq"              // PostgreSQL
	_ "modernc.org/sqlite"             // SQLite
)

func main() {
	r := gin.Default()
	r.LoadHTMLGlob("templates/*")

	// Роут для главной страницы
	r.GET("/", func(c *gin.Context) {
		tmpl, err := template.ParseFiles("templates/index.html")
		if err != nil {
			c.String(http.StatusInternalServerError, "Error load template")
			return
		}
		tmpl.Execute(c.Writer, nil)
	})
	r.POST("/test", func(c *gin.Context) {
		c.HTML(http.StatusInternalServerError, "result.html", gin.H{
			"Error": "test",
		})
	})
	// Роут для обработки SQL-запроса
	r.POST("/query", func(c *gin.Context) {
		driver := c.PostForm("driver")
		server := c.PostForm("server")
		username := c.PostForm("username")
		password := c.PostForm("password")
		database := c.PostForm("database")
		query := c.PostForm("query")

		// Обработка адреса сервера и порта
		serverAddress := server
		defaultPort := ""

		switch driver {
		case "postgres":
			defaultPort = "5432"
		case "mysql":
			defaultPort = "3306"
		case "clickhouse":
			defaultPort = "9000"
		}

		// Проверяем, содержит ли адрес порт
		if !strings.Contains(serverAddress, ":") && defaultPort != "" {
			serverAddress = fmt.Sprintf("%s:%s", serverAddress, defaultPort)
		}

		log.Printf("Attempting to connect to %s database at %s", driver, server)

		// Build DSN with increased timeouts
		var dsn string
		switch driver {
		case "postgres":
			// Увеличенные таймауты и добавлены параметры повторных попыток
			dsn = fmt.Sprintf(
				"postgres://%s:%s@%s/%s?sslmode=disable&connect_timeout=30"+
					"&pool_timeout=30&statement_timeout=60"+
					"&tcp_user_timeout=60000", // milliseconds
				username, url.QueryEscape(password), serverAddress, database,
			)
		case "mysql":
			host, port, err := net.SplitHostPort(serverAddress)
			if err != nil {
				log.Printf("Error splitting host/port: %v", err)
				host = serverAddress
				port = defaultPort
			}
			dsn = fmt.Sprintf(
				"%s:%s@tcp(%s:%s)/%s?timeout=30s&readTimeout=30s&writeTimeout=30s",
				username, password, host, port, database,
			)
		case "clickhouse":
			host, port, err := net.SplitHostPort(serverAddress)
			if err != nil {
				log.Printf("Error splitting host/port: %v", err)
				host = serverAddress
				port = defaultPort
			}
			dsn = fmt.Sprintf("tcp://%s:%s?username=%s&password=%s&database=%s&read_timeout=30&write_timeout=30",
				host, port, username, password, database)
		default:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Unsupported database driver",
			})
			return
		}

		// Создаем контекст с таймаутом
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// Функция для проверки доступности сервера
		checkServer := func() error {
			conn, err := net.DialTimeout("tcp", server, 5*time.Second)
			if err != nil {
				return fmt.Errorf("cannot connect to server %s: %v", server, err)
			}
			if conn != nil {
				defer conn.Close()
			}
			return nil
		}

		// Проверяем доступность сервера перед попыткой подключения к БД
		if err := checkServer(); err != nil {
			log.Printf("Server connectivity check failed: %v", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": fmt.Sprintf("Database server is not accessible: %v", err),
			})
			return
		}

		// Конфигурация для повторных попыток
		maxRetries := 3
		var db *sql.DB
		var err error

		// Пытаемся подключиться с повторными попытками
		for i := 0; i < maxRetries; i++ {
			log.Printf("Attempting database connection (attempt %d of %d)", i+1, maxRetries)

			db, err = sql.Open(driver, dsn)
			if err != nil {
				log.Printf("Failed to open database connection: %v", err)
				continue
			}

			// Настройка пула соединений
			db.SetMaxOpenConns(25)
			db.SetMaxIdleConns(25)
			db.SetConnMaxLifetime(5 * time.Minute)
			db.SetConnMaxIdleTime(30 * time.Second)

			// Проверка подключения
			err = db.PingContext(ctx)
			if err == nil {
				break // Успешное подключение
			}

			log.Printf("Database ping failed (attempt %d): %v", i+1, err)
			db.Close()

			if i < maxRetries-1 {
				time.Sleep(time.Second * time.Duration(i+1)) // Экспоненциальная задержка
			}
		}

		if err != nil {
			log.Printf("All connection attempts failed: %v", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": fmt.Sprintf("Failed to connect to database after %d attempts: %v", maxRetries, err),
			})
			return
		}
		defer db.Close()

		// Выполнение запроса с контекстом
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			log.Printf("Query execution failed: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("Query error: %v", err),
			})
			return
		}
		defer rows.Close()

		// Get column names
		cols, err := rows.Columns()
		if err != nil {
			c.HTML(http.StatusInternalServerError, "result.html", gin.H{
				"Error": fmt.Sprintf("Failed to get columns: %v", err),
			})
			return
		}

		// Process rows with proper type handling
		var rowsData []map[string]interface{}
		for rows.Next() {
			// Create a slice of interface{} to hold the values
			values := make([]interface{}, len(cols))
			valuePtrs := make([]interface{}, len(cols))
			for i := range values {
				valuePtrs[i] = &values[i]
			}

			if err := rows.Scan(valuePtrs...); err != nil {
				c.HTML(http.StatusInternalServerError, "result.html", gin.H{
					"Error": fmt.Sprintf("Failed to scan row: %v", err),
				})
				return
			}

			// Convert row to map
			row := make(map[string]interface{})
			for i, col := range cols {
				var v interface{}
				val := values[i]
				b, ok := val.([]byte)
				if ok {
					v = string(b)
				} else {
					v = val
				}
				row[col] = v
			}
			rowsData = append(rowsData, row)
		}

		if err := rows.Err(); err != nil {
			c.HTML(http.StatusInternalServerError, "result.html", gin.H{
				"Error": fmt.Sprintf("Error during row iteration: %v", err),
			})
			return
		}

		// Send JSON response instead of HTML for better data handling
		c.JSON(http.StatusOK, gin.H{
			"columns": cols,
			"rows":    rowsData,
			"status":  "success",
		})
	})

	log.Println("Сервер запущен на http://localhost:8081")
	r.Run(":8081")
}
