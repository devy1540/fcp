package fcp;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;

import com.google.genai.Client;
import org.junit.jupiter.api.Test;

class GoogleGenAiCompatibilityTest {

    @Test
    void demoBackendGoogleGenAiVersionUsesEnvironmentBaseUrl() {
        String endpoint = required("FCP_HTTP_ENDPOINT");
        assertEquals(endpoint, required("GOOGLE_GEMINI_BASE_URL"));

        try (var client = Client.builder().apiKey("fcp-local").build()) {
            var response = client.models.generateContent(
                    "gemini-2.5-flash",
                    "This prompt must stay inside the local FCP process.",
                    null);
            assertEquals("FCP local generated response", response.text());
            assertNotNull(response.usageMetadata().orElse(null));
        }
    }

    private static String required(String name) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) {
            throw new IllegalStateException(name + " is required");
        }
        return value;
    }
}
