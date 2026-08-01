plugins {
    java
}

dependencies {
    testImplementation("com.google.cloud:google-cloud-storage:2.70.0")
    testImplementation("com.google.cloud:google-cloud-pubsub:1.152.0")
    testImplementation("com.google.cloud:google-cloud-secretmanager:2.94.0")
    testImplementation("com.google.cloud:google-cloud-kms:2.97.0")
	testImplementation("com.google.cloud:google-cloud-iamcredentials:2.94.0")
    testImplementation("com.google.genai:google-genai:1.63.0")
    testImplementation("org.junit.jupiter:junit-jupiter:6.1.2")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher:6.1.2")
}

java {
    toolchain.languageVersion.set(JavaLanguageVersion.of(21))
}
