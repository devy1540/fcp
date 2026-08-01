plugins {
    kotlin("jvm")
}

dependencies {
    testImplementation(platform("com.google.cloud:spring-cloud-gcp-dependencies:8.1.0"))
    testImplementation("com.google.cloud:google-cloud-firestore")
    testImplementation("com.google.cloud:google-cloud-secretmanager:2.94.0")
    testImplementation("software.amazon.awssdk:dynamodb:2.49.5")
    testImplementation("software.amazon.awssdk:dynamodb-enhanced:2.49.5")
    testImplementation("software.amazon.awssdk:sqs:2.49.5")
    testImplementation("software.amazon.awssdk:sts:2.49.5")
    testImplementation("org.junit.jupiter:junit-jupiter:6.1.2")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher:6.1.2")
}

kotlin {
    jvmToolchain(21)
}
