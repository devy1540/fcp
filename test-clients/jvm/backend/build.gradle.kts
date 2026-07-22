plugins {
    java
}

dependencies {
    testImplementation("com.google.cloud:google-cloud-storage:2.68.0")
    testImplementation("com.google.cloud:google-cloud-pubsub:1.140.1")
    testImplementation("com.google.cloud:google-cloud-secretmanager:2.52.0")
    testImplementation("com.google.cloud:google-cloud-kms:2.96.0")
	testImplementation("com.google.cloud:google-cloud-iamcredentials:2.51.0")
    testImplementation("com.google.genai:google-genai:1.58.0")
    testImplementation("org.junit.jupiter:junit-jupiter:5.11.4")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher:1.11.4")
}

java {
    toolchain.languageVersion.set(JavaLanguageVersion.of(21))
}
