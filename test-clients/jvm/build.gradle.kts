plugins {
    kotlin("jvm") version "2.4.10" apply false
}

allprojects {
    repositories {
        mavenCentral()
    }
    dependencyLocking {
        lockAllConfigurations()
    }
}

subprojects {
    tasks.withType<Test>().configureEach {
        useJUnitPlatform()
        testLogging {
            events("passed", "skipped", "failed")
        }
    }
}
