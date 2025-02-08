package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql" // MySQL
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite" // SQLite
)

func main() {
	r := gin.Default()
	r.LoadHTMLGlob("templates/*")
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))

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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var dsn string
		var db *sql.DB
		var err error

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
			dsn = fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true",
				username, password, serverAddress, database)
			db, err = sql.Open("mysql", dsn)
			if err != nil {
				log.Printf("Failed to open database connection: %v", err)
				c.JSON(500, gin.H{"error": "Database connection error"})
				return
			}
			defer db.Close()

			// Test connection
			err = db.Ping()
			if err != nil {
				log.Printf("Database connection failed: %v", err)
				c.JSON(500, gin.H{"error": "Failed to connect to database"})
				return
			}

			// Execute query
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
			columns, err := rows.Columns()
			if err != nil {
				log.Printf("Failed to get column names: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Failed to retrieve column names",
				})
				return
			}

			// Process rows
			var rowsData []map[string]interface{}
			for rows.Next() {
				values := make([]interface{}, len(columns))
				scanArgs := make([]interface{}, len(columns))
				for i := range values {
					scanArgs[i] = &values[i]
				}

				if err := rows.Scan(scanArgs...); err != nil {
					log.Printf("Failed to scan row: %v", err)
					c.JSON(http.StatusInternalServerError, gin.H{
						"error": "Failed to scan row",
					})
					return
				}

				row := make(map[string]interface{})
				for i, col := range columns {
					if b, ok := values[i].([]byte); ok {
						row[col] = string(b)
					} else {
						row[col] = values[i]
					}
				}
				rowsData = append(rowsData, row)
			}

			if err := rows.Err(); err != nil {
				log.Printf("Error during row iteration: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Error processing rows",
				})
				return
			}

			c.HTML(
				http.StatusOK,
				"result.html",
				gin.H{
					"Columns": columns,
					"Rows":    rowsData,
					"status":  "success",
				},
			)
		case "clickhouse":
			conn, err := clickhouse.Open(&clickhouse.Options{
				Addr: []string{serverAddress},
				Auth: clickhouse.Auth{
					Database: database,
					Username: username,
					Password: password,
				},
				DialTimeout: 5 * time.Second,
			})
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("failed to connect to ClickHouse: %v", err),
				})
				return
			}
			defer conn.Close()

			rows, err := conn.Query(ctx, query)
			fmt.Println("TEST", err)
			if err != nil && err.Error() != "EOF" {
				log.Printf("Query execution failed: %v", err)
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("Query error: %v", err),
				})
				return
			}
			defer rows.Close()

			// Get column names and types
			columns := rows.Columns()
			columnTypes := rows.ColumnTypes()

			// Process rows
			var rowsData []map[string]interface{}
			for rows.Next() {
				// Create properly typed scan destinations
				scanArgs := make([]interface{}, len(columns))
				for i, ct := range columnTypes {
					switch ct.DatabaseTypeName() {
					case "String":
						scanArgs[i] = new(string)
					case "UInt8", "UInt16", "UInt32":
						scanArgs[i] = new(uint32)
					case "UInt64":
						scanArgs[i] = new(uint64)
					case "Int8", "Int16", "Int32":
						scanArgs[i] = new(int32)
					case "Int64":
						scanArgs[i] = new(int64)
					case "Float32":
						scanArgs[i] = new(float32)
					case "Float64":
						scanArgs[i] = new(float64)
					case "DateTime":
						scanArgs[i] = new(time.Time)
					case "Date":
						scanArgs[i] = new(time.Time)
					default:
						scanArgs[i] = new(interface{})
					}
				}

				if err := rows.Scan(scanArgs...); err != nil {
					log.Printf("Failed to scan row: %v", err)
					c.JSON(http.StatusInternalServerError, gin.H{
						"error": fmt.Sprintf("Failed to scan row: %v", err),
					})
					return
				}

				// Convert scanned values to map
				row := make(map[string]interface{})
				for i, col := range columns {
					switch v := scanArgs[i].(type) {
					case *string:
						row[col] = *v
					case *uint32:
						row[col] = *v
					case *uint64:
						row[col] = *v
					case *int32:
						row[col] = *v
					case *int64:
						row[col] = *v
					case *float32:
						row[col] = *v
					case *float64:
						row[col] = *v
					case *time.Time:
						row[col] = *v
					case *interface{}:
						row[col] = *v
					default:
						row[col] = v
					}
				}
				rowsData = append(rowsData, row)
			}

			if err := rows.Err(); err != nil {
				log.Printf("Error during row iteration: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Error processing rows",
				})
				return
			}

			c.HTML(
				http.StatusOK,
				"result.html",
				gin.H{
					"Columns": columns,
					"Rows":    rowsData,
					"status":  "success",
				},
			)
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
