plugins {
    kotlin("jvm")
}

dependencies {
    testImplementation(platform("com.google.cloud:spring-cloud-gcp-dependencies:7.4.6"))
    testImplementation("com.google.cloud:google-cloud-firestore")
    testImplementation("com.google.cloud:google-cloud-secretmanager:2.59.0")
    testImplementation("software.amazon.awssdk:dynamodb:2.33.9")
    testImplementation("software.amazon.awssdk:dynamodb-enhanced:2.33.9")
    testImplementation("software.amazon.awssdk:sqs:2.33.9")
    testImplementation("software.amazon.awssdk:sts:2.33.9")
    testImplementation("org.junit.jupiter:junit-jupiter:5.11.4")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher:1.11.4")
}

kotlin {
    jvmToolchain(21)
}
