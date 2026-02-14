---
name: Go Unit Testing & Coverage
description: Best practices for writing unit tests, mocking dependencies, and measuring code coverage in Go projects.
---

# Go Unit Testing & Coverage Standardization

## 1. Testing Philosophy
- **Table-Driven Tests**: Use table-driven tests for all unit tests to cover multiple scenarios (happy path, edge cases, partial failures) efficiently.
- **Isolation**: Unit tests must mock all external dependencies (Database, APIs). Use `github.com/stretchr/testify/mock`.
- **Assertions**: Use `github.com/stretchr/testify/assert` for readable assertions.

## 2. Dependency Mocking
Generate mocks for interfaces using `mockery` or manually implement them using `testify/mock`.

### Example Mock
```go
import "github.com/stretchr/testify/mock"

type MockEventRepo struct {
    mock.Mock
}

func (m *MockEventRepo) DeductInventory(ctx context.Context, eventID, quantity int) error {
    args := m.Called(ctx, eventID, quantity)
    return args.Error(0)
}
```

## 3. Service Layer Testing Pattern
```go
func TestBookingService_BookTicket(t *testing.T) {
    tests := []struct {
        name          string
        req           BookRequest
        mockSetup     func(*MockEventRepo, *MockOrderRepo)
        expectedError error
    }{
        {
            name: "Success",
            req:  BookRequest{UserID: 1, EventID: 1, Quantity: 2},
            mockSetup: func(e *MockEventRepo, o *MockOrderRepo) {
                e.On("DeductInventory", mock.Anything, 1, 2).Return(nil)
                o.On("Create", mock.Anything, mock.AnythingOfType("*domain.Order")).Return(nil)
            },
            expectedError: nil,
        },
        {
            name: "Sold Out",
            req:  BookRequest{UserID: 1, EventID: 1, Quantity: 2},
            mockSetup: func(e *MockEventRepo, o *MockOrderRepo) {
                e.On("DeductInventory", mock.Anything, 1, 2).Return(domain.ErrSoldOut)
            },
            expectedError: domain.ErrSoldOut,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Setup Mocks & Service
            // Run Test
            // Assert Expectations
        })
    }
}
```

## 4. HTTP Handler Testing (Gin)
Use `httptest.NewRecorder()` to capture responses.

```go
func TestHandler_BookConfig(t *testing.T) {
    // Setup
    gin.SetMode(gin.TestMode)
    w := httptest.NewRecorder()
    c, _ := gin.CreateTestContext(w)
    
    // Create Request
    reqBody := bytes.NewBufferString(`{"user_id": 1, "event_id": 1, "quantity": 1}`)
    c.Request, _ = http.NewRequest("POST", "/book", reqBody)

    // Invoke Handler
    handler.HandleBook(c)

    // Assert
    assert.Equal(t, http.StatusOK, w.Code)
}
```

## 5. Coverage Analysis
To ensure high code quality, run tests with coverage enabled.

### Run Tests with Coverage
```bash
go test -coverprofile=coverage.out ./internal/...
```

### View Coverage Report (HTML)
```bash
go tool cover -html=coverage.out
```

### Check Function Coverage
```bash
go tool cover -func=coverage.out
```
