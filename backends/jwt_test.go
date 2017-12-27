package backends

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	jwt "github.com/dgrijalva/jwt-go"
	. "github.com/smartystreets/goconvey/convey"
)

var username = "test"

//Hash generated by the pw utility
var userPassHash = "PBKDF2$sha512$100000$os24lcPr9cJt2QDVWssblQ==$BK1BQ2wbwU1zNxv3Ml3wLuu5//hPop3/LvaPYjjCwdBvnpwusnukJPpcXQzyyjOlZdieXTx6sXAcX4WnZRZZnw=="

var jwtSecret = "some_jwt_secret"

// Generate the token.
var now = time.Now()
var nowSecondsSinceEpoch = now.Unix()
var expSecondsSinceEpoch int64 = nowSecondsSinceEpoch + int64(time.Hour*24/time.Second)

var jwtToken = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
	"iss":      "jwt-test",
	"aud":      "jwt-test",
	"nbf":      nowSecondsSinceEpoch,
	"exp":      expSecondsSinceEpoch,
	"sub":      "user",
	"username": username,
})

var wrongJwtToken = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
	"iss":      "jwt-test",
	"aud":      "jwt-test",
	"nbf":      nowSecondsSinceEpoch,
	"exp":      expSecondsSinceEpoch,
	"sub":      "user",
	"username": "wrong_user",
})

func TestLocalPostgresJWT(t *testing.T) {

	Convey("Creating a token should return a nil error", t, func() {
		token, err := jwtToken.SignedString([]byte(jwtSecret))
		So(err, ShouldBeNil)

		//Initialize JWT in local mode.
		authOpts := make(map[string]string)
		authOpts["jwt_remote"] = "false"
		authOpts["jwt_db"] = "postgres"
		authOpts["jwt_secret"] = jwtSecret
		authOpts["jwt_userquery"] = "select count(*) from test_user where username = $1 limit 1"
		authOpts["jwt_superquery"] = "select count(*) from test_user where username = $1 and is_admin = true"
		authOpts["jwt_aclquery"] = "SELECT test_acl.topic FROM test_acl, test_user WHERE test_user.username = $1 AND test_acl.test_user_id = test_user.id AND rw >= $2"
		authOpts["pg_userquery"] = "mock_string"
		authOpts["pg_superquery"] = "mock_string"
		authOpts["pg_aclquery"] = "mock_string"

		//Give necessary postgres options.
		authOpts["pg_host"] = "localhost"
		authOpts["pg_port"] = "5432"
		authOpts["pg_dbname"] = "go_auth_test"
		authOpts["pg_user"] = "go_auth_test"
		authOpts["pg_password"] = "go_auth_test"

		Convey("Given correct option NewJWT returns an instance of jwt backend", func() {
			jwt, err := NewJWT(authOpts)
			So(err, ShouldBeNil)

			//Empty DB
			jwt.Postgres.DB.MustExec("delete from test_user where 1 = 1")
			jwt.Postgres.DB.MustExec("delete from test_acl where 1 = 1")

			//Now test everything.

			insertQuery := "INSERT INTO test_user(username, password_hash, is_admin) values($1, $2, $3) returning id"

			userID := 0

			iqErr := jwt.Postgres.DB.Get(&userID, insertQuery, username, userPassHash, true)

			So(iqErr, ShouldBeNil)
			So(userID, ShouldBeGreaterThan, 0)

			Convey("Given a correct token, it should correctly authenticate it", func() {

				authenticated := jwt.GetUser(token, "")
				So(authenticated, ShouldBeTrue)

			})

			Convey("Given an incorrect token, it should not authenticate it", func() {

				wrongToken, err := wrongJwtToken.SignedString([]byte(jwtSecret))
				So(err, ShouldBeNil)

				authenticated := jwt.GetUser(wrongToken, "")
				So(authenticated, ShouldBeFalse)

			})

			Convey("Given a token that is admin, super user should pass", func() {
				superuser := jwt.GetSuperuser(token)
				So(superuser, ShouldBeTrue)
			})

			//Now create some acls and test topics

			strictAcl := "test/topic/1"
			singleLevelAcl := "test/topic/+"
			hierarchyAcl := "test/#"

			clientID := "test_client"

			aclID := 0
			aclQuery := "INSERT INTO test_acl(test_user_id, topic, rw) values($1, $2, $3) returning id"
			aqErr := jwt.Postgres.DB.Get(&aclID, aclQuery, userID, strictAcl, 1)
			So(aqErr, ShouldBeNil)

			Convey("Given only strict acl in DB, an exact match should work and and inexact one not", func() {

				testTopic1 := `test/topic/1`
				testTopic2 := `test/topic/2`

				tt1 := jwt.CheckAcl(token, testTopic1, clientID, 1)
				tt2 := jwt.CheckAcl(token, testTopic2, clientID, 1)

				So(tt1, ShouldBeTrue)
				So(tt2, ShouldBeFalse)

			})

			Convey("Given read only privileges, a pub check should fail", func() {

				testTopic1 := "test/topic/1"
				tt1 := jwt.CheckAcl(token, testTopic1, clientID, 2)
				So(tt1, ShouldBeFalse)

			})

			Convey("Given wildcard subscriptions against strict db acl, acl checks should fail", func() {

				tt1 := jwt.CheckAcl(token, singleLevelAcl, clientID, 1)
				tt2 := jwt.CheckAcl(token, hierarchyAcl, clientID, 1)

				So(tt1, ShouldBeFalse)
				So(tt2, ShouldBeFalse)

			})

			//Now insert single level topic to check against.

			aqErr = jwt.Postgres.DB.Get(&aclID, aclQuery, userID, singleLevelAcl, 1)
			So(aqErr, ShouldBeNil)

			Convey("Given a topic not strictly present that matches a db single level wildcard, acl check should pass", func() {
				tt1 := jwt.CheckAcl(token, "test/topic/whatever", clientID, 1)
				So(tt1, ShouldBeTrue)
			})

			//Now insert hierarchy wildcard to check against.

			aqErr = jwt.Postgres.DB.Get(&aclID, aclQuery, userID, hierarchyAcl, 1)
			So(aqErr, ShouldBeNil)

			Convey("Given a topic not strictly present that matches a hierarchy wildcard, acl check should pass", func() {
				tt1 := jwt.CheckAcl(token, "test/what/ever", clientID, 1)
				So(tt1, ShouldBeTrue)
			})

			//Empty db
			jwt.Postgres.DB.MustExec("delete from test_user where 1 = 1")
			jwt.Postgres.DB.MustExec("delete from test_acl where 1 = 1")

		})

	})

}

func TestLocalMysqlJWT(t *testing.T) {

	Convey("Creating a token should return a nil error", t, func() {
		token, err := jwtToken.SignedString([]byte(jwtSecret))
		So(err, ShouldBeNil)

		//Initialize JWT in local mode.
		authOpts := make(map[string]string)
		authOpts["jwt_remote"] = "false"
		authOpts["jwt_db"] = "mysql"
		authOpts["jwt_secret"] = jwtSecret
		authOpts["jwt_userquery"] = "select count(*) from test_user where username = ? limit 1"
		authOpts["jwt_superquery"] = "select count(*) from test_user where username = ? and is_admin = true"
		authOpts["jwt_aclquery"] = "SELECT test_acl.topic FROM test_acl, test_user WHERE test_user.username = ? AND test_acl.test_user_id = test_user.id AND rw >= ?"
		authOpts["mysql_userquery"] = "mock_string"
		authOpts["mysql_superquery"] = "mock_string"
		authOpts["mysql_aclquery"] = "mock_string"

		//Give necessary postgres options.
		authOpts["mysql_host"] = "localhost"
		authOpts["mysql_port"] = "3306"
		authOpts["mysql_dbname"] = "go_auth_test"
		authOpts["mysql_user"] = "go_auth_test"
		authOpts["mysql_password"] = "go_auth_test"

		Convey("Given correct option NewJWT returns an instance of jwt backend", func() {
			jwt, err := NewJWT(authOpts)
			So(err, ShouldBeNil)

			//Empty DB
			jwt.Mysql.DB.MustExec("delete from test_user where 1 = 1")
			jwt.Mysql.DB.MustExec("delete from test_acl where 1 = 1")

			//Now test everything.

			insertQuery := "INSERT INTO test_user(username, password_hash, is_admin) values(?, ?, ?)"

			userID := int64(0)

			res, iqErr := jwt.Mysql.DB.Exec(insertQuery, username, userPassHash, true)
			So(iqErr, ShouldBeNil)

			userID, idErr := res.LastInsertId()

			So(idErr, ShouldBeNil)
			So(userID, ShouldBeGreaterThan, 0)

			Convey("Given a correct token, it should correctly authenticate it", func() {

				authenticated := jwt.GetUser(token, "")
				So(authenticated, ShouldBeTrue)

			})

			Convey("Given an incorrect token, it should not authenticate it", func() {

				wrongToken, err := wrongJwtToken.SignedString([]byte(jwtSecret))
				So(err, ShouldBeNil)

				authenticated := jwt.GetUser(wrongToken, "")
				So(authenticated, ShouldBeFalse)

			})

			Convey("Given a token that is admin, super user should pass", func() {
				superuser := jwt.GetSuperuser(token)
				So(superuser, ShouldBeTrue)
			})

			//Now create some acls and test topics

			strictAcl := "test/topic/1"
			singleLevelAcl := "test/topic/+"
			hierarchyAcl := "test/#"

			clientID := "test_client"

			aclID := int64(0)
			aclQuery := "INSERT INTO test_acl(test_user_id, topic, rw) values(?, ?, ?)"
			res, aqErr := jwt.Mysql.DB.Exec(aclQuery, userID, strictAcl, 1)
			So(aqErr, ShouldBeNil)
			aclID, aclIdErr := res.LastInsertId()
			So(aclIdErr, ShouldBeNil)
			So(aclID, ShouldBeGreaterThan, 0)

			Convey("Given only strict acl in DB, an exact match should work and and inexact one not", func() {

				testTopic1 := `test/topic/1`
				testTopic2 := `test/topic/2`

				tt1 := jwt.CheckAcl(token, testTopic1, clientID, 1)
				tt2 := jwt.CheckAcl(token, testTopic2, clientID, 1)

				So(tt1, ShouldBeTrue)
				So(tt2, ShouldBeFalse)

			})

			Convey("Given read only privileges, a pub check should fail", func() {

				testTopic1 := "test/topic/1"
				tt1 := jwt.CheckAcl(token, testTopic1, clientID, 2)
				So(tt1, ShouldBeFalse)

			})

			Convey("Given wildcard subscriptions against strict db acl, acl checks should fail", func() {

				tt1 := jwt.CheckAcl(token, singleLevelAcl, clientID, 1)
				tt2 := jwt.CheckAcl(token, hierarchyAcl, clientID, 1)

				So(tt1, ShouldBeFalse)
				So(tt2, ShouldBeFalse)

			})

			//Now insert single level topic to check against.

			_, aqErr = jwt.Mysql.DB.Exec(aclQuery, userID, singleLevelAcl, 1)
			So(aqErr, ShouldBeNil)

			Convey("Given a topic not strictly present that matches a db single level wildcard, acl check should pass", func() {
				tt1 := jwt.CheckAcl(token, "test/topic/whatever", clientID, 1)
				So(tt1, ShouldBeTrue)
			})

			//Now insert hierarchy wildcard to check against.

			_, aqErr = jwt.Mysql.DB.Exec(aclQuery, userID, hierarchyAcl, 1)
			So(aqErr, ShouldBeNil)

			Convey("Given a topic not strictly present that matches a hierarchy wildcard, acl check should pass", func() {
				tt1 := jwt.CheckAcl(token, "test/what/ever", clientID, 1)
				So(tt1, ShouldBeTrue)
			})

			//Empty db
			jwt.Mysql.DB.MustExec("delete from test_user where 1 = 1")
			jwt.Mysql.DB.MustExec("delete from test_acl where 1 = 1")

		})

	})

}

func TestJWTAllJsonServer(t *testing.T) {

	topic := "test/topic"
	var acc = int64(1)
	clientId := "test_client"

	token, _ := jwtToken.SignedString([]byte(jwtSecret))
	wrongToken, _ := wrongJwtToken.SignedString([]byte(jwtSecret))

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		httpResponse := &HTTPResponse{
			Ok:    true,
			Error: "",
		}

		var jsonResponse []byte

		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")

		gToken := r.Header.Get("authorization")

		if r.URL.Path == "/user" || r.URL.Path == "/superuser" {
			if token == gToken {
				httpResponse.Ok = true
				httpResponse.Error = ""
			} else {
				httpResponse.Ok = false
				httpResponse.Error = "Wrong token."
			}
		} else if r.URL.Path == "/acl" {

			var data interface{}
			var params map[string]interface{}

			body, _ := ioutil.ReadAll(r.Body)
			defer r.Body.Close()

			uErr := json.Unmarshal(body, &data)

			if uErr != nil {
				httpResponse.Ok = false
				httpResponse.Error = "Json unmarshal error"

			} else {

				params = data.(map[string]interface{})
				paramsAcc := int64(params["acc"].(float64))

				if token == gToken && params["topic"].(string) == topic && params["clientid"].(string) == clientId && paramsAcc <= acc {
					httpResponse.Ok = true
					httpResponse.Error = ""
				} else {
					httpResponse.Ok = false
					httpResponse.Error = "Acl check failed."
				}

			}
		}

		jsonResponse, mjErr := json.Marshal(httpResponse)
		if mjErr != nil {
			w.Write([]byte("error"))
		}

		w.Write(jsonResponse)

	}))

	defer mockServer.Close()

	log.Printf("Trying host: %s\n", mockServer.URL)

	authOpts := make(map[string]string)
	authOpts["jwt_remote"] = "true"
	authOpts["jwt_params_mode"] = "json"
	authOpts["jwt_response_mode"] = "json"
	authOpts["jwt_host"] = strings.Replace(mockServer.URL, "http://", "", -1)
	authOpts["jwt_port"] = ""
	authOpts["jwt_getuser_uri"] = "/user"
	authOpts["jwt_superuser_uri"] = "/superuser"
	authOpts["jwt_aclcheck_uri"] = "/acl"

	Convey("Given correct options an http backend instance should be returned", t, func() {
		hb, err := NewJWT(authOpts)
		So(err, ShouldBeNil)

		Convey("Given correct password/username, get user should return true", func() {

			authenticated := hb.GetUser(token, "")
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect password/username, get user should return false", func() {

			authenticated := hb.GetUser(wrongToken, "")
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct username, get superuser should return true", func() {

			authenticated := hb.GetSuperuser(token)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect username, get superuser should return false", func() {

			authenticated := hb.GetSuperuser(wrongToken)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct topic, username, client id and acc, acl check should return true", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 1)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given an acc that requires more privileges than the user has, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 2)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a topic not present in acls, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, "fake/topic", clientId, 1)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a clientId that doesn't match, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, "fake_client_id", 1)
			So(authenticated, ShouldBeFalse)

		})

	})

}

func TestJWTJsonStatusOnlyServer(t *testing.T) {

	topic := "test/topic"
	var acc = int64(1)
	clientId := "test_client"
	token, _ := jwtToken.SignedString([]byte(jwtSecret))
	wrongToken, _ := wrongJwtToken.SignedString([]byte(jwtSecret))

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		var data interface{}
		var params map[string]interface{}

		body, _ := ioutil.ReadAll(r.Body)
		defer r.Body.Close()

		uErr := json.Unmarshal(body, &data)

		if uErr != nil {
			w.WriteHeader(http.StatusBadRequest)
		}

		gToken := r.Header.Get("authorization")

		if r.URL.Path == "/user" || r.URL.Path == "/superuser" {
			if token == gToken {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		} else if r.URL.Path == "/acl" {
			params = data.(map[string]interface{})
			paramsAcc := int64(params["acc"].(float64))
			if token == gToken && params["topic"].(string) == topic && params["clientid"].(string) == clientId && paramsAcc <= acc {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}

	}))

	defer mockServer.Close()

	log.Printf("Trying host: %s\n", mockServer.URL)

	authOpts := make(map[string]string)
	authOpts["jwt_remote"] = "true"
	authOpts["jwt_params_mode"] = "json"
	authOpts["jwt_response_mode"] = "status"
	authOpts["jwt_host"] = strings.Replace(mockServer.URL, "http://", "", -1)
	authOpts["jwt_port"] = ""
	authOpts["jwt_getuser_uri"] = "/user"
	authOpts["jwt_superuser_uri"] = "/superuser"
	authOpts["jwt_aclcheck_uri"] = "/acl"

	Convey("Given correct options an http backend instance should be returned", t, func() {
		hb, err := NewJWT(authOpts)
		So(err, ShouldBeNil)

		Convey("Given correct password/username, get user should return true", func() {

			authenticated := hb.GetUser(token, "")
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect password/username, get user should return false", func() {

			authenticated := hb.GetUser(wrongToken, "")
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct username, get superuser should return true", func() {

			authenticated := hb.GetSuperuser(token)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect username, get superuser should return false", func() {

			authenticated := hb.GetSuperuser(wrongToken)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct topic, username, client id and acc, acl check should return true", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 1)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given an acc that requires more privileges than the user has, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 2)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a topic not present in acls, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, "fake/topic", clientId, 1)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a clientId that doesn't match, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, "fake_client_id", 1)
			So(authenticated, ShouldBeFalse)

		})

	})

}

func TestJWTJsonTextResponseServer(t *testing.T) {

	topic := "test/topic"
	var acc = int64(1)
	clientId := "test_client"
	token, _ := jwtToken.SignedString([]byte(jwtSecret))
	wrongToken, _ := wrongJwtToken.SignedString([]byte(jwtSecret))

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		var data interface{}
		var params map[string]interface{}

		body, _ := ioutil.ReadAll(r.Body)
		defer r.Body.Close()

		uErr := json.Unmarshal(body, &data)

		w.WriteHeader(http.StatusOK)

		if uErr != nil {
			w.Write([]byte(uErr.Error()))
		}

		gToken := r.Header.Get("authorization")

		if r.URL.Path == "/user" || r.URL.Path == "/superuser" {
			if token == gToken {
				w.Write([]byte("ok"))
			} else {
				w.Write([]byte("Wrong credentials."))
			}
		} else if r.URL.Path == "/acl" {
			params = data.(map[string]interface{})
			paramsAcc := int64(params["acc"].(float64))
			if token == gToken && params["topic"].(string) == topic && params["clientid"].(string) == clientId && paramsAcc <= acc {
				w.Write([]byte("ok"))
			} else {
				w.Write([]byte("Acl check failed."))
			}
		} else {
			w.Write([]byte("Path not found."))
		}

	}))

	defer mockServer.Close()

	log.Printf("Trying host: %s\n", mockServer.URL)

	authOpts := make(map[string]string)
	authOpts["jwt_remote"] = "true"
	authOpts["jwt_params_mode"] = "json"
	authOpts["jwt_response_mode"] = "text"
	authOpts["jwt_host"] = strings.Replace(mockServer.URL, "http://", "", -1)
	authOpts["jwt_port"] = ""
	authOpts["jwt_getuser_uri"] = "/user"
	authOpts["jwt_superuser_uri"] = "/superuser"
	authOpts["jwt_aclcheck_uri"] = "/acl"

	Convey("Given correct options an http backend instance should be returned", t, func() {
		hb, err := NewJWT(authOpts)
		So(err, ShouldBeNil)

		Convey("Given correct password/username, get user should return true", func() {

			authenticated := hb.GetUser(token, "")
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect password/username, get user should return false", func() {

			authenticated := hb.GetUser(wrongToken, "")
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct username, get superuser should return true", func() {

			authenticated := hb.GetSuperuser(token)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect username, get superuser should return false", func() {

			authenticated := hb.GetSuperuser(wrongToken)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct topic, username, client id and acc, acl check should return true", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 1)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given an acc that requires more privileges than the user has, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 2)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a topic not present in acls, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, "fake/topic", clientId, 1)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a clientId that doesn't match, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, "fake_client_id", 1)
			So(authenticated, ShouldBeFalse)

		})

	})

}

func TestJWTFormJsonResponseServer(t *testing.T) {

	topic := "test/topic"
	var acc = int64(1)
	clientId := "test_client"
	token, _ := jwtToken.SignedString([]byte(jwtSecret))
	wrongToken, _ := wrongJwtToken.SignedString([]byte(jwtSecret))

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		httpResponse := &HTTPResponse{
			Ok:    true,
			Error: "",
		}

		pfErr := r.ParseForm()
		if pfErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var jsonResponse []byte
		var params = r.Form
		log.Printf("Got params: %v\n", params)

		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")

		gToken := r.Header.Get("authorization")

		if r.URL.Path == "/user" || r.URL.Path == "/superuser" {
			if token == gToken {
				httpResponse.Ok = true
				httpResponse.Error = ""
			} else {
				httpResponse.Ok = false
				httpResponse.Error = "Wrong credentials."
			}
		} else if r.URL.Path == "/acl" {
			paramsAcc, _ := strconv.ParseInt(params["acc"][0], 10, 64)
			if token == gToken && params["topic"][0] == topic && params["clientid"][0] == clientId && paramsAcc <= acc {
				httpResponse.Ok = true
				httpResponse.Error = ""
			} else {
				httpResponse.Ok = false
				httpResponse.Error = "Acl check failed."
			}
		}

		jsonResponse, mjErr := json.Marshal(httpResponse)
		if mjErr != nil {
			w.Write([]byte("error"))
		}

		w.Write(jsonResponse)

	}))

	defer mockServer.Close()

	log.Printf("Trying host: %s\n", mockServer.URL)

	authOpts := make(map[string]string)
	authOpts["jwt_remote"] = "true"
	authOpts["jwt_params_mode"] = "form"
	authOpts["jwt_response_mode"] = "json"
	authOpts["jwt_host"] = strings.Replace(mockServer.URL, "http://", "", -1)
	authOpts["jwt_port"] = ""
	authOpts["jwt_getuser_uri"] = "/user"
	authOpts["jwt_superuser_uri"] = "/superuser"
	authOpts["jwt_aclcheck_uri"] = "/acl"

	Convey("Given correct options an http backend instance should be returned", t, func() {
		hb, err := NewJWT(authOpts)
		So(err, ShouldBeNil)

		Convey("Given correct password/username, get user should return true", func() {

			authenticated := hb.GetUser(token, "")
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect password/username, get user should return false", func() {

			authenticated := hb.GetUser(wrongToken, "")
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct username, get superuser should return true", func() {

			authenticated := hb.GetSuperuser(token)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect username, get superuser should return false", func() {

			authenticated := hb.GetSuperuser(wrongToken)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct topic, username, client id and acc, acl check should return true", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 1)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given an acc that requires more privileges than the user has, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 2)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a topic not present in acls, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, "fake/topic", clientId, 1)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a clientId that doesn't match, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, "fake_client_id", 1)
			So(authenticated, ShouldBeFalse)

		})

	})

}

func TestJWTFormStatusOnlyServer(t *testing.T) {

	topic := "test/topic"
	var acc = int64(1)
	clientId := "test_client"
	token, _ := jwtToken.SignedString([]byte(jwtSecret))
	wrongToken, _ := wrongJwtToken.SignedString([]byte(jwtSecret))

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		pfErr := r.ParseForm()
		if pfErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var params = r.Form

		gToken := r.Header.Get("authorization")

		if r.URL.Path == "/user" || r.URL.Path == "/superuser" {
			if token == gToken {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		} else if r.URL.Path == "/acl" {
			paramsAcc, _ := strconv.ParseInt(params["acc"][0], 10, 64)
			if token == gToken && params["topic"][0] == topic && params["clientid"][0] == clientId && paramsAcc <= acc {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}

	}))

	defer mockServer.Close()

	log.Printf("Trying host: %s\n", mockServer.URL)

	authOpts := make(map[string]string)
	authOpts["jwt_remote"] = "true"
	authOpts["jwt_params_mode"] = "form"
	authOpts["jwt_response_mode"] = "status"
	authOpts["jwt_host"] = strings.Replace(mockServer.URL, "http://", "", -1)
	authOpts["jwt_port"] = ""
	authOpts["jwt_getuser_uri"] = "/user"
	authOpts["jwt_superuser_uri"] = "/superuser"
	authOpts["jwt_aclcheck_uri"] = "/acl"

	Convey("Given correct options an http backend instance should be returned", t, func() {
		hb, err := NewJWT(authOpts)
		So(err, ShouldBeNil)

		Convey("Given correct password/username, get user should return true", func() {

			authenticated := hb.GetUser(token, "")
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect password/username, get user should return false", func() {

			authenticated := hb.GetUser(wrongToken, "")
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct username, get superuser should return true", func() {

			authenticated := hb.GetSuperuser(token)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect username, get superuser should return false", func() {

			authenticated := hb.GetSuperuser(wrongToken)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct topic, username, client id and acc, acl check should return true", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 1)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given an acc that requires more privileges than the user has, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 2)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a topic not present in acls, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, "fake/topic", clientId, 1)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a clientId that doesn't match, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, "fake_client_id", 1)
			So(authenticated, ShouldBeFalse)

		})

	})

}

func TestJWTFormTextResponseServer(t *testing.T) {

	topic := "test/topic"
	var acc = int64(1)
	clientId := "test_client"
	token, _ := jwtToken.SignedString([]byte(jwtSecret))
	wrongToken, _ := wrongJwtToken.SignedString([]byte(jwtSecret))

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		w.WriteHeader(http.StatusOK)

		pfErr := r.ParseForm()
		if pfErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var params = r.Form

		gToken := r.Header.Get("authorization")

		if r.URL.Path == "/user" || r.URL.Path == "/superuser" {
			if token == gToken {
				w.Write([]byte("ok"))
			} else {
				w.Write([]byte("Wrong credentials."))
			}
		} else if r.URL.Path == "/acl" {
			paramsAcc, _ := strconv.ParseInt(params["acc"][0], 10, 64)
			if token == gToken && params["topic"][0] == topic && params["clientid"][0] == clientId && paramsAcc <= acc {
				w.Write([]byte("ok"))
			} else {
				w.Write([]byte("Acl check failed."))
			}
		} else {
			w.Write([]byte("Path not found."))
		}

	}))

	defer mockServer.Close()

	log.Printf("Trying host: %s\n", mockServer.URL)

	authOpts := make(map[string]string)
	authOpts["jwt_remote"] = "true"
	authOpts["jwt_params_mode"] = "form"
	authOpts["jwt_response_mode"] = "text"
	authOpts["jwt_host"] = strings.Replace(mockServer.URL, "http://", "", -1)
	authOpts["jwt_port"] = ""
	authOpts["jwt_getuser_uri"] = "/user"
	authOpts["jwt_superuser_uri"] = "/superuser"
	authOpts["jwt_aclcheck_uri"] = "/acl"

	Convey("Given correct options an http backend instance should be returned", t, func() {
		hb, err := NewJWT(authOpts)
		So(err, ShouldBeNil)

		Convey("Given correct password/username, get user should return true", func() {

			authenticated := hb.GetUser(token, "")
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect password/username, get user should return false", func() {

			authenticated := hb.GetUser(wrongToken, "")
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct username, get superuser should return true", func() {

			authenticated := hb.GetSuperuser(token)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given incorrect username, get superuser should return false", func() {

			authenticated := hb.GetSuperuser(wrongToken)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given correct topic, username, client id and acc, acl check should return true", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 1)
			So(authenticated, ShouldBeTrue)

		})

		Convey("Given an acc that requires more privileges than the user has, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, clientId, 2)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a topic not present in acls, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, "fake/topic", clientId, 1)
			So(authenticated, ShouldBeFalse)

		})

		Convey("Given a clientId that doesn't match, check acl should return false", func() {

			authenticated := hb.CheckAcl(token, topic, "fake_client_id", 1)
			So(authenticated, ShouldBeFalse)

		})

	})

}
