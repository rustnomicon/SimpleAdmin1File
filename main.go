package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"

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
		log.Default().Printf("driver: %s, server: %s, username: %s, password: %s, database: %s, query: %s", driver, server, username, password, database, query)
		var dsn string
		switch driver {
		case "postgres":
			dsn = fmt.Sprint("postgres://", username, ":", password, "@", server, "/", database, "?sslmode=disable")
		case "mysql":
			dsn = fmt.Sprintf("%s:%s@tcp(%s)/%s", username, password, server, database)
		case "sqlite":
			dsn = database
		case "clickhouse":
			dsn = fmt.Sprintf("tcp://%s?username=%s&password=%s&database=%s",
				server, username, password, database)
		case "duckdb":
			dsn = database
		default:
			c.HTML(http.StatusInternalServerError, "result.html", gin.H{
				"Error": "Unsupported database driver",
			})
			c.Header("Content-Type", "text/html")
			return
		}

		// Database connection (same as before)
		db, err := sql.Open(driver, dsn)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "result.html", gin.H{
				"Error": fmt.Sprintf("Connection error: %v", err),
			})
			return
		} else {
			c.HTML(http.StatusOK, "dialog.html", gin.H{
				"Content": "Connection success",
			})
			c.Header("Content-Type", "text/html")
		}

		defer db.Close()

		// Execute query
		rows, err := db.Query(query)
		if err != nil {
			c.HTML(http.StatusBadRequest, "result.html", gin.H{
				"Error": fmt.Sprintf("Query error: %v", err),
			})

			return
		}
		defer rows.Close()

		// get answer
		c.HTML(http.StatusOK, "result.html", gin.H{
			"Test": rows,
		})
		// Get column names
		cols, err := rows.Columns()
		if err != nil {
			c.HTML(http.StatusInternalServerError, "result.html", gin.H{
				"Error": fmt.Sprintf("Column error: %v", err),
			})
			return
		}

		// Process rows
		var rowsData [][]interface{}
		for rows.Next() {
			columns := make([]interface{}, len(cols))
			columnPointers := make([]interface{}, len(cols))
			for i := range columns {
				columnPointers[i] = &columns[i]
			}

			if err := rows.Scan(columnPointers...); err != nil {
				c.HTML(http.StatusInternalServerError, "result.html", gin.H{
					"Error": fmt.Sprintf("Scan error: %v", err),
				})
				return
			}

			rowsData = append(rowsData, columns)
		}

		// Check for row iteration errors
		if err := rows.Err(); err != nil {
			c.HTML(http.StatusInternalServerError, "result.html", gin.H{
				"Error": fmt.Sprintf("Row error: %v", err),
			})
			return
		}

		// Render template with data
		c.HTML(http.StatusOK, "result.html", gin.H{
			"Columns": cols,
			"Rows":    rowsData,
		})
	})

	log.Println("Сервер запущен на http://localhost:8081")
	r.Run(":8081")
}
