package fcp;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import org.junit.jupiter.api.Test;

class FcmCompatibilityTest {

    @Test
    void demoNotificationFcpPushPathCapturesMessage() throws Exception {
        String endpoint = required("FCP_HTTP_ENDPOINT");
        String token = "notifications-jvm-" + System.nanoTime();
        String payload = """
                {"message":{"token":"%s","data":{"source":"notifications"}}}
                """.formatted(token);

        var client = HttpClient.newHttpClient();
        var request = HttpRequest.newBuilder()
                .uri(URI.create(endpoint + "/v1/projects/fcp-local/messages:send"))
                .header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(payload))
                .build();
        var response = client.send(request, HttpResponse.BodyHandlers.ofString());

        assertEquals(200, response.statusCode());
        assertTrue(response.body().contains("projects/fcp-local/messages/"));

        var captured = client.send(
                HttpRequest.newBuilder()
                        .uri(URI.create(endpoint + "/_fcp/fcm/messages?project=fcp-local"))
                        .GET()
                        .build(),
                HttpResponse.BodyHandlers.ofString());
        assertEquals(200, captured.statusCode());
        assertTrue(captured.body().contains(token));
        assertTrue(captured.body().contains("notifications"));
    }

    private static String required(String name) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) {
            throw new IllegalStateException(name + " is required");
        }
        return value;
    }
}
