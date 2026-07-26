plugins {
    kotlin("jvm") version "2.2.21" apply false
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
