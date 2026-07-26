package server

import "github.com/devy1540/fcp/internal/compatibility"

func dashboardServicesFromCatalog(catalog []compatibility.Service) []dashboardService {
	services := make([]dashboardService, 0, len(catalog))
	for _, service := range catalog {
		operations := make([]dashboardOperationVerification, 0, len(service.Operations))
		for _, operation := range service.Operations {
			operations = append(operations, dashboardOperationVerification{
				Name:   operation.Name,
				Status: operation.Status,
				Scope:  operation.Scope,
			})
		}
		services = append(services, dashboardService{
			ID:          service.ID,
			Name:        service.Name,
			Provider:    service.Provider,
			Description: service.Description,
			Status:      "READY",
			Verification: dashboardVerification{
				Level:       service.Level,
				Label:       compatibility.VerificationLabel(service.Level),
				Evidence:    service.Evidence,
				Source:      compatibility.Source,
				Operations:  operations,
				Limitations: append([]string(nil), service.Limitations...),
			},
			Resources: []dashboardResource{},
		})
	}
	return services
}
