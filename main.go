package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "github.com/ClickHouse/clickhouse-go" // ClickHouse
	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql" // MySQL
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite" // SQLite
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

		log.Printf("Attempting to connect to %s database at %s", driver, serverAddress)

		// Создаем контекст с таймаутом
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		switch driver {
		case "postgres":

			// Construct connection string for pgx
			connConfig := &pgxpool.Config{}
			connConfig, err := pgxpool.ParseConfig(fmt.Sprintf(
				"postgres://%s:%s@%s/%s?sslmode=disable",
				username, url.QueryEscape(password), serverAddress, database,
			))
			if err != nil {
				log.Printf("Failed to parse pgx config: %v", err)
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("Invalid connection configuration: %v", err),
				})
				return
			}

			// Configure the connection pool
			connConfig.MaxConns = 25
			connConfig.MaxConnLifetime = 5 * time.Minute
			connConfig.MaxConnIdleTime = 30 * time.Second

			// Create connection pool with retries
			var pool *pgxpool.Pool
			maxRetries := 3
			for i := 0; i < maxRetries; i++ {
				log.Printf("Attempting database connection (attempt %d of %d)", i+1, maxRetries)

				pool, err = pgxpool.NewWithConfig(ctx, connConfig)
				if err == nil {
					// Test the connection
					err = pool.Ping(ctx)
					if err == nil {
						break // Successfully connected
					}
				}

				log.Printf("Database connection failed (attempt %d): %v", i+1, err)
				if pool != nil {
					pool.Close()
				}

				if i < maxRetries-1 {
					time.Sleep(time.Second * time.Duration(i+1))
				}
			}

			if err != nil {
				log.Printf("All connection attempts failed: %v", err)
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"error": fmt.Sprintf("Failed to connect to database after %d attempts: %v", maxRetries, err),
				})
				return
			}
			defer pool.Close()

			// Execute query
			rows, err := pool.Query(ctx, query)
			if err != nil {
				log.Printf("Query execution failed: %v", err)
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("Query error: %v", err),
				})
				return
			}
			defer rows.Close()

			// Get column descriptions
			fields := rows.FieldDescriptions()
			cols := make([]string, len(fields))
			for i, field := range fields {
				cols[i] = string(field.Name)
			}

			// Process rows
			var rowsData []map[string]interface{}
			for rows.Next() {
				values, err := rows.Values()
				if err != nil {
					c.HTML(http.StatusInternalServerError, "result.html", gin.H{
						"Error": fmt.Sprintf("Failed to get row values: %v", err),
					})
					return
				}

				row := make(map[string]interface{})
				for i, col := range cols {
					row[col] = values[i]
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
			// c.JSON(http.StatusOK, gin.H{
			// 	"columns": cols,
			// 	"rows":    rowsData,
			// 	"status":  "success",
			// })
			c.HTML(
				http.StatusOK,
				"result.html",
				gin.H{
					"Columns": cols,
					"Rows":    rowsData,
					"status":  "success",
				},
			)
		case "mysql":
			// host, port, err := net.SplitHostPort(serverAddress)
			// if err != nil {
			// 	log.Printf("Error splitting host/port: %v", err)
			// 	host = serverAddress
			// 	port = defaultPort
			// }
			// dsn = fmt.Sprintf(
			// 	"%s:%s@tcp(%s:%s)/%s?timeout=30s&readTimeout=30s&writeTimeout=30s",
			// 	username, password, host, port, database,
			// )
		case "clickhouse":
			// host, port, err := net.SplitHostPort(serverAddress)
			// if err != nil {
			// 	log.Printf("Error splitting host/port: %v", err)
			// 	host = serverAddress
			// 	port = defaultPort
			// }
			// dsn = fmt.Sprintf("tcp://%s:%s?username=%s&password=%s&database=%s&read_timeout=30&write_timeout=30",
			// 	host, port, username, password, database)
		default:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Unsupported database driver",
			})
			return
		}

	})

	log.Println("Сервер запущен на http://localhost:8081")
	r.Run(":8081")
}
