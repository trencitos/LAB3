package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	pb "admin/proto/grpc-server/proto"
	"google.golang.org/grpc"
)

// adminClient es un wrapper para el cliente gRPC
type adminClient struct {
	client pb.MatchmakerClient
}

// 1. Mostrar estado de Servidores y Colas de Jugadores
func (c *adminClient) viewSystemStatus() {
	log.Println("Solicitando estado del sistema al Matchmaker...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := c.client.AdminGetSystemStatus(ctx, &pb.AdminRequest{})
	if err != nil {
		log.Printf("Error al obtener el estado del sistema: %v", err)
		return
	}

	// Formatear y presentar la información [bonito y ordenado jeje :)]
	fmt.Println("\n=======================================================")
	fmt.Println("              ESTADO DEL SISTEMA")
	fmt.Println("=======================================================")
	fmt.Printf("Reloj Vectorial del Matchmaker: %v\n", res.GetVectorClock())

	// Mostrar estado de los servidores
	fmt.Println("\n--- Servidores de Partida ---")
	if len(res.GetServers()) == 0 {
		fmt.Println("No hay servidores registrados.")
	} else {
		fmt.Println("ID\t\tEstado\t\tDirección\t\tPartida Actual")
		fmt.Println("-------------------------------------------------------")
		for _, server := range res.GetServers() {
			fmt.Printf("%s\t%s\t\t%s\t%s\n", server.ServerId, server.Status, server.Address, server.CurrentMatchId)
		}
	}

	// Mostrar estado de la cola de jugadores
	fmt.Println("\n--- Cola de Jugadores ---")
	if len(res.GetPlayerQueue()) == 0 {
		fmt.Println("La cola de emparejamiento está vacía.")
	} else {
		fmt.Println("ID Jugador\t\tTiempo en cola (s)")
		fmt.Println("-------------------------------------------------------")
		for _, player := range res.GetPlayerQueue() {
			fmt.Printf("%s\t\t%d\n", player.PlayerId, player.TimeInQueueSeconds)
		}
	}
}

// Menú interactivo principal
func (c *adminClient) runMenu() {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Println("\n=== Menú del Administrador ===")
		fmt.Println("1. Ver Estado del Sistema (Servidores y Colas)")
		fmt.Println("2. Salir")
		fmt.Print("> ")

		if !scanner.Scan() {
			break
		}
		choice := strings.TrimSpace(scanner.Text())

		switch choice {
		case "1":
			c.viewSystemStatus()
		case "2":
			fmt.Println("Saliendo...")
			return
		default:
			fmt.Println("Opción no válida. Intente de nuevo.")
		}
	}
}

func main() {
	// Configuración de logging para mensajes claros
	log.SetFlags(log.Ltime | log.Lshortfile)

	// Conexión gRPC al Matchmaker Central
	matchmakerAddr := "10.35.168.122:50051"
	conn, err := grpc.Dial(matchmakerAddr, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("No se pudo conectar al Matchmaker en %s: %v", matchmakerAddr, err)
	}
	defer conn.Close()

	log.Println("Conectado al Matchmaker.")

	// Crear el cliente
	client := &adminClient{
		client: pb.NewMatchmakerClient(conn),
	}

	// Iniciar el menú interactivo
	client.runMenu()
}